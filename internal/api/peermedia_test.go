package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/overfin/server/internal/api"
	"github.com/overfin/server/internal/federation"
	"github.com/overfin/server/internal/slots"
	"github.com/overfin/server/internal/store"
)

// A paired server asking for a playback's media here: GET only, a Range of
// MSFP's three forms only (media section 2); this server's own viewers'
// browsers keep what they had (any one range Jellyfin answers).
func TestPeerTicketMediaRules(t *testing.T) {
	ctx := context.Background()
	var st *store.Store
	var b *federation.Service
	srv, fake := newServerWith(t, options{store: &st, federation: &b})
	fake.setDirect(true)
	st.PutCatalog(ctx, []store.CatalogItem{
		{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "949", LibraryID: "films"},
	}, 1)
	st.RefreshTitleKeys(ctx)
	st.SetLibraries(ctx, []store.Library{{ID: "films", Item: []byte(`{"Name":"Films"}`)}})
	a, bOnA, _ := viewer(t, b, srv.Client())
	var s peerSession
	if status, err := a.Call(ctx, bOnA, "/api/v1/federation/play", map[string]any{"title": "movie/tmdb/949", "capabilities": map[string]any{}}, &s); status != 200 {
		t.Fatalf("play: %d %v", status, err)
	}
	ask := func(method, rangeHeader string) int {
		req, _ := http.NewRequest(method, s.Stream.URL, nil)
		if rangeHeader != "" {
			req.Header.Set("Range", rangeHeader)
		}
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		res.Body.Close()
		return res.StatusCode
	}
	if code := ask("HEAD", ""); code != http.StatusNotFound {
		t.Errorf("HEAD of a paired server: %d", code)
	}
	for _, h := range []string{"bytes=-0", "bytes=0-1,5-9", "bytes=-"} {
		if code := ask("GET", h); code != http.StatusRequestedRangeNotSatisfiable {
			t.Errorf("%s of a paired server: %d", h, code)
		}
	}
	if code := ask("GET", "bytes=0-"); code != http.StatusOK {
		t.Errorf("a range of a paired server: %d", code)
	}

	// This server's own viewer: its browser's Range goes on as before.
	status, body := post(t, srv, "/api/v1/playback", fmt.Sprintf(`{"itemId":"item_%032x","capabilities":{}}`, 1))
	var own peerSession
	json.Unmarshal([]byte(body), &own)
	if status != 201 {
		t.Fatalf("own playback: %d %s", status, body)
	}
	req, _ := http.NewRequest("GET", srv.URL+own.Stream.URL, nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Remote-User", subject)
	req.Header.Set("Range", "bytes=-0")
	res, err := client(srv, subject).Do(req)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("this server's own viewer's range: %v %v", res, err)
	}
	res.Body.Close()
}

// A paired server fetches at most so many parts of one playback at once
// here; past the bound and its queue, it is told busy (503), and the
// next fetch, once one ended, goes through (MSFP media, section 2.4).
func TestPeerTicketFetchesBounded(t *testing.T) {
	defer api.SetMediaBounds(&slots.Slots{Size: 1, Wait: 10 * time.Millisecond}, &slots.Slots{Size: 8, Queue: 8, Wait: time.Second})()
	ctx := context.Background()
	var st *store.Store
	var b *federation.Service
	srv, fake := newServerWith(t, options{store: &st, federation: &b})
	fake.setDirect(true)
	st.PutCatalog(ctx, []store.CatalogItem{
		{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "949", LibraryID: "films"},
	}, 1)
	st.RefreshTitleKeys(ctx)
	st.SetLibraries(ctx, []store.Library{{ID: "films", Item: []byte(`{"Name":"Films"}`)}})
	a, bOnA, _ := viewer(t, b, srv.Client())
	var s peerSession
	if status, err := a.Call(ctx, bOnA, "/api/v1/federation/play", map[string]any{"title": "movie/tmdb/949", "capabilities": map[string]any{}}, &s); status != 200 {
		t.Fatalf("play: %d %v", status, err)
	}
	open := func() *http.Response {
		res, err := srv.Client().Get(s.Stream.URL)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	first := open() // the fake's file flows until it's closed
	if first.StatusCode != 200 {
		t.Fatalf("first: %d", first.StatusCode)
	}
	if second := open(); second.StatusCode != http.StatusServiceUnavailable {
		second.Body.Close()
		t.Fatalf("a second at once: %d", second.StatusCode)
	}
	first.Body.Close()
	if !eventually(t, func() bool {
		res := open()
		res.Body.Close()
		return res.StatusCode == 200
	}) {
		t.Fatal("once the first ended")
	}
}

// What A relays of B's playback: a 206 only for the range its viewer
// asked, B's 416 and 503 passed on, anything else a 404, a Range outside
// MSFP's forms refused without asking B, a playlist too large a 404, and
// no more fetches at once than its bound (MSFP media, section 2).
func TestRelayChecksWhatItRelays(t *testing.T) {
	var mu sync.Mutex
	var mode string
	var asked []string
	x := pairForPlayWith(t, true, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, "/api/v1/stream/") {
				h.ServeHTTP(w, r)
				return
			}
			mu.Lock()
			m := mode
			asked = append(asked, r.Method+" "+r.Header.Get("Range"))
			mu.Unlock()
			switch {
			case strings.HasSuffix(r.URL.Path, "/slow.m3u8"):
				w.Write([]byte("#EXTM3U\n"))
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
				case <-time.After(5 * time.Second):
				}
			case strings.HasSuffix(r.URL.Path, ".m3u8"):
				w.Write([]byte("#EXTM3U\n" + strings.Repeat("#EXTINF:6,\n", 3<<20/11)))
			case m == "good":
				w.Header().Set("Content-Range", "bytes 0-9/100")
				w.Header().Set("Content-Length", "10")
				w.WriteHeader(http.StatusPartialContent)
				w.Write([]byte("0123456789"))
			case m == "elsewhere":
				w.Header().Set("Content-Range", "bytes 10-19/100")
				w.WriteHeader(http.StatusPartialContent)
				w.Write([]byte("0123456789"))
			case m == "suffix":
				w.Header().Set("Content-Range", "bytes 90-99/100")
				w.WriteHeader(http.StatusPartialContent)
				w.Write([]byte("0123456789"))
			case m == "416":
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			case m == "503":
				w.WriteHeader(http.StatusServiceUnavailable)
			case m == "teapot":
				w.WriteHeader(http.StatusTeapot)
				w.Write([]byte("B's own words"))
			default:
				h.ServeHTTP(w, r)
			}
		})
	})
	status, body := post(t, x.srvA, "/api/v1/playback", `{"titleId":"title_movie_tmdb_949","capabilities":{},"audioLanguage":"fr"}`)
	var s peerSession
	json.Unmarshal([]byte(body), &s)
	if status != 201 {
		t.Fatalf("play on A: %d %s", status, body)
	}
	fetch := func(path, m, rangeHeader string) (int, string) {
		mu.Lock()
		mode = m
		mu.Unlock()
		req, _ := http.NewRequest("GET", x.srvA.URL+path, nil)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Remote-User", subject)
		if rangeHeader != "" {
			req.Header.Set("Range", rangeHeader)
		}
		res, err := client(x.srvA, subject).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return res.StatusCode, string(b)
	}
	for _, c := range []struct {
		mode, rangeHeader string
		want              int
	}{
		{"good", "bytes=0-9", http.StatusPartialContent},
		{"good", "bytes=0-", http.StatusPartialContent},
		{"good", "", http.StatusNotFound}, // a 206 to no range
		{"good", "bytes=0-4", http.StatusNotFound},
		{"elsewhere", "bytes=0-", http.StatusNotFound},
		{"suffix", "bytes=-10", http.StatusPartialContent},
		{"suffix", "bytes=-20", http.StatusNotFound},
		{"416", "bytes=500-", http.StatusRequestedRangeNotSatisfiable},
		{"503", "bytes=0-", http.StatusServiceUnavailable},
		{"teapot", "", http.StatusNotFound},
	} {
		code, got := fetch(s.Stream.URL, c.mode, c.rangeHeader)
		if code != c.want || strings.Contains(got, "B's own words") {
			t.Errorf("%s, %q: %d %q, want %d", c.mode, c.rangeHeader, code, got, c.want)
		}
	}
	// A Range outside MSFP's forms: refused by A, B never asked.
	mu.Lock()
	before := len(asked)
	mu.Unlock()
	for _, h := range []string{"bytes=-0", "bytes=0-1,5-9"} {
		if code, _ := fetch(s.Stream.URL, "good", h); code != http.StatusRequestedRangeNotSatisfiable {
			t.Errorf("%s: %d", h, code)
		}
	}
	mu.Lock()
	if len(asked) != before {
		t.Errorf("B asked for a refused range: %v", asked[before:])
	}
	for _, a := range asked {
		if !strings.HasPrefix(a, "GET ") {
			t.Errorf("A asked B with %s", a)
		}
	}
	mu.Unlock()
	// A playlist past 2 MiB: a failed fetch.
	if code, _ := fetch(strings.TrimSuffix(s.Stream.URL, "file")+"v0.m3u8", "", ""); code != http.StatusNotFound {
		t.Errorf("a playlist too large: %d", code)
	}

	// A playlist that trickles: cut after its time, a failed fetch.
	undo := api.SetSmallReadTime(100 * time.Millisecond)
	start := time.Now()
	if code, _ := fetch(strings.TrimSuffix(s.Stream.URL, "file")+"slow.m3u8", "", ""); code != http.StatusNotFound || time.Since(start) > 2*time.Second {
		t.Errorf("a playlist that trickles: %d after %v", code, time.Since(start))
	}
	undo()

	// One fetch at a time for this playback, none waiting: a second at
	// once is busy.
	defer api.SetMediaBounds(&slots.Slots{Size: 8, Queue: 8, Wait: time.Second}, &slots.Slots{Size: 1, Wait: 10 * time.Millisecond})()
	req, _ := http.NewRequest("GET", x.srvA.URL+s.Stream.URL, nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Remote-User", subject)
	mu.Lock()
	mode = ""
	mu.Unlock()
	held, err := client(x.srvA, subject).Do(req)
	if err != nil || held.StatusCode != 200 {
		t.Fatalf("held: %v %v", held, err)
	}
	if code, _ := fetch(s.Stream.URL, "", ""); code != http.StatusServiceUnavailable {
		t.Errorf("a second fetch at once: %d", code)
	}
	held.Body.Close()
}
