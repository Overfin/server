package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/msfp-io/msfp-go/wire"
)

// A delivery's segments come from Jellyfin by the names in Overfin's own
// playlist only, with the segment format's own type (never Jellyfin's),
// whole to this server's viewers (no Range passed on to Jellyfin), and
// only to the playback's own session.
func TestDeliverySegments(t *testing.T) {
	p := playedServer(t, options{})
	code, a, raw := play(t, p.srv, `{"titleId":"title_movie_tmdb_949","versionId":"`+heatCopy+`","delivery":"remux","mediaType":"video/mp2t"}`)
	if code != 201 {
		t.Fatalf("play: %d %s", code, raw)
	}
	base := "/api/v1/stream/" + a.Ticket + "/"

	get := func(path, rng string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", p.srv.URL+path, nil)
		req.Header.Set("Remote-User", subject)
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		res, err := client(p.srv, subject).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, string(b)
	}

	res, body := get(base+"seg0.ts", "")
	if res.StatusCode != 200 || body != tsSegment || res.Header.Get("Content-Type") != "video/mp2t" {
		t.Fatalf("seg0.ts: %d %q %s", res.StatusCode, body, res.Header.Get("Content-Type"))
	}
	// A viewer's Range isn't passed on: the segment comes whole.
	res, body = get(base+"seg0.ts", "bytes=2-5")
	if res.StatusCode != 200 || body != tsSegment {
		t.Errorf("seg0.ts with a Range: %d %q", res.StatusCode, body)
	}
	p.fake.mu.Lock()
	ranges := append([]string(nil), p.fake.segRanges...)
	p.fake.mu.Unlock()
	if len(ranges) != 2 || ranges[0] != "" || ranges[1] != "" {
		t.Errorf("Range headers Jellyfin got: %q", ranges)
	}

	// Names Overfin's playlist doesn't give, and Jellyfin's own paths,
	// aren't served.
	for _, name := range []string{"seg1.ts", "seg0.mp4", "init.mp4", "hls1/main/0.ts", "../seg0.ts", "seg0.ts?x=1", "SEG0.ts"} {
		if res, _ := get(base+name, ""); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", name, res.StatusCode)
		}
	}
	// Another person's session can't use the ticket.
	if code, _ := getAs(t, p.srv, "someone-else", base+"seg0.ts"); code != http.StatusNotFound && code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Errorf("another person: %d", code)
	}
}

// A paired server's segments, through A: A's viewer reads A's playlists,
// whose names A gives, and gets B's segment, fetched by A's engine from B
// (B's ticket never leaves A), B's from Jellyfin.
func TestPeerSegmentThroughA(t *testing.T) {
	x := pairForPlay(t)
	var page struct{ Copies []wire.Copy }
	get(t, x.srvA, "/api/v1/titles/title_movie_tmdb_949", &page)
	status, s := x.start(t, `{"titleId":"title_movie_tmdb_949","versionId":"`+page.Copies[0].ID+`","delivery":"remux","mediaType":"video/mp2t"}`)
	if status != 201 || s.Protocol != "hls" {
		t.Fatalf("remux from B: %d %+v", status, s)
	}
	base := "/api/v1/stream/" + s.Ticket + "/"
	code, master := viewerGet(t, x.srvA, base+"master.m3u8")
	if code != 200 {
		t.Fatalf("master: %d", code)
	}
	media := ""
	for _, l := range strings.Split(master, "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			media = l
			break
		}
	}
	code, list := viewerGet(t, x.srvA, base+media)
	if code != 200 {
		t.Fatalf("media playlist %q: %d %s\nmaster: %s", media, code, list, master)
	}
	seg := ""
	for _, l := range strings.Split(list, "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			seg = l
			break
		}
	}
	if seg == "" || strings.Contains(seg, live0Ticket(t, x)) || strings.Contains(seg, "/") {
		t.Fatalf("A's segment name: %q", seg)
	}
	if code, body := viewerGet(t, x.srvA, base+seg); code != 200 || body != tsSegment {
		t.Errorf("segment through A: %d %q", code, body)
	}
	// A range of it, in each of MSFP's three forms: B asks Jellyfin for
	// it as written, A's viewer gets those bytes.
	n := len(tsSegment)
	for h, want := range map[string]string{"bytes=3-9": tsSegment[3:10], "bytes=5-": tsSegment[5:], "bytes=-4": tsSegment[n-4:]} {
		req, _ := http.NewRequest("GET", x.srvA.URL+base+seg, nil)
		req.Header.Set("Remote-User", subject)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Range", h)
		res, err := client(x.srvA, subject).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 206 || string(b) != want {
			t.Errorf("%s through A: %d %q, want %q", h, res.StatusCode, b, want)
		}
		x.played.fake.mu.Lock()
		asked := x.played.fake.segRanges[len(x.played.fake.segRanges)-1]
		x.played.fake.mu.Unlock()
		if asked != h {
			t.Errorf("%s asked of Jellyfin as %q", h, asked)
		}
	}
	// Jellyfin failing under B: refusing it, a not found for A's viewer;
	// failing otherwise, busy; never Jellyfin's own words.
	for status, want := range map[int]int{403: 404, 404: 404, 500: 503, 502: 503} {
		x.played.fake.mu.Lock()
		x.played.fake.segStatus = status
		x.played.fake.mu.Unlock()
		code, body := viewerGet(t, x.srvA, base+seg)
		if code != want || strings.Contains(body, "/media") {
			t.Errorf("Jellyfin answering %d: A's viewer got %d %q, want %d", status, code, body, want)
		}
	}
	x.played.fake.mu.Lock()
	x.played.fake.segStatus = 0
	x.played.fake.mu.Unlock()
}

func live0Ticket(t *testing.T, x *twoServers) string {
	t.Helper()
	live := livePlaybacks(x.onB)
	if len(live) != 1 {
		t.Fatalf("B's playbacks: %d", len(live))
	}
	return live[0].Ticket
}
