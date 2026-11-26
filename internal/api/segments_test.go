package api_test

import (
	"io"
	"net/http"
	"testing"
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
