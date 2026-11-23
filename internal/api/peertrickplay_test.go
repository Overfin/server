package api_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// sheetStandIn stands in for B on its previews' sheets once given a body:
// a server sending what Overfin's own would never.
type sheetStandIn struct{ body atomic.Pointer[[]byte] }

func (o *sheetStandIn) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b := o.body.Load(); b != nil && strings.HasPrefix(r.URL.Path, "/api/v1/stream/") && strings.Contains(r.URL.Path, "/trickplay/") {
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(*b)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func jpegOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Members by their exact names: federation answers are read so.
type previewSession struct {
	ID     string `json:"id"`
	Stream struct {
		URL string `json:"url"`
	} `json:"stream"`
	Trickplay *struct {
		TileWidth  int      `json:"tileWidth"`
		TileHeight int      `json:"tileHeight"`
		Columns    int      `json:"columns"`
		Rows       int      `json:"rows"`
		IntervalMs int      `json:"intervalMs"`
		TileCount  int      `json:"tileCount"`
		Sheets     []string `json:"sheets"`
	} `json:"trickplay"`
}

// startOnA starts Heat on A, which only B has.
func startOnA(t *testing.T, x *twoServers) (previewSession, string) {
	t.Helper()
	status, body := post(t, x.srvA, "/api/v1/playback", `{"titleId":"title_movie_tmdb_949","capabilities":{}}`)
	var s previewSession
	json.Unmarshal([]byte(body), &s)
	if status != 201 {
		t.Fatalf("play on A: %d %s", status, body)
	}
	return s, body
}

// A paired server's previews are part of its playback: B offers them with
// the playback's ticket (the engine's rules for it are msfp's tests); A
// offers them at its own playback's route, each sheet fetched from B and
// re-encoded, and refuses sheets that aren't what B announced.
func TestPeerPlaybackPreviews(t *testing.T) {
	t.Skip("federation off until phase 7")
	var standIn sheetStandIn
	x := pairForPlayWith(t, standIn.wrap)

	// A's viewer: the same previews, at A's own addresses.
	s, body := startOnA(t, x)
	tp := s.Trickplay
	if tp == nil || tp.TileWidth != 320 || tp.TileHeight != 136 || tp.Rows != 10 || tp.IntervalMs != 10000 || len(tp.Sheets) != 7 ||
		tp.Sheets[6] != "/api/v1/playback/"+s.ID+"/trickplay/6" || strings.Contains(body, x.srvB.URL) {
		t.Fatalf("A's previews: %s", body)
	}
	if code, got := getAs(t, x.srvA, subject, tp.Sheets[6]); code != 200 || !strings.HasPrefix(got, "\xff\xd8") {
		t.Fatalf("the sheet through A: %d", code)
	}
	if code, _ := getAs(t, x.srvA, "other-subject", tp.Sheets[6]); code != http.StatusNotFound {
		t.Errorf("another account's: %d", code)
	}
	for _, bad := range []string{"7", "06", "-1"} {
		if code, _ := getAs(t, x.srvA, subject, "/api/v1/playback/"+s.ID+"/trickplay/"+bad); code != http.StatusNotFound {
			t.Errorf("sheet %s: %d", bad, code)
		}
	}
	// A's view of the playback says the same.
	var view previewSession
	if status := get(t, x.srvA, "/api/v1/playback/"+s.ID, &view); status != 200 || view.Trickplay == nil || view.Trickplay.Sheets[6] != tp.Sheets[6] {
		t.Fatalf("A's view: %d %+v", status, view.Trickplay)
	}

	// What B sends, A re-encodes: nothing but the pixels comes through.
	marker := "a trailer that is not pixels"
	sheet := append(jpegOf(t, 64, 32), marker...)
	standIn.body.Store(&sheet)
	s, _ = startOnA(t, x)
	if code, got := getAs(t, x.srvA, subject, s.Trickplay.Sheets[6]); code != 200 || !strings.HasPrefix(got, "\xff\xd8") || strings.Contains(got, marker) {
		t.Fatalf("a re-encoded sheet: %d, marker kept %v", code, strings.Contains(got, marker))
	}
	// Too many bytes, wider than a sheet of its tiles (10 × 320), not a
	// JPEG: refused.
	var pngSheet bytes.Buffer
	png.Encode(&pngSheet, image.NewRGBA(image.Rect(0, 0, 64, 32)))
	for name, bad := range map[string][]byte{
		"too large": append(jpegOf(t, 64, 32), make([]byte, 9<<20)...),
		"too wide":  jpegOf(t, 3201, 8),
		"too tall":  jpegOf(t, 8, 1361),
		"a PNG":     pngSheet.Bytes(),
	} {
		standIn.body.Store(&bad)
		s, _ = startOnA(t, x)
		if code, _ := getAs(t, x.srvA, subject, s.Trickplay.Sheets[6]); code != http.StatusNotFound {
			t.Errorf("%s: %d", name, code)
		}
	}
}

// A paired server without previews for a copy, or with previews outside
// what MSFP allows, plays without them, as an older Overfin's do.
func TestPeerPlaybackWithoutPreviews(t *testing.T) {
	t.Skip("federation off until phase 7")
	x := pairForPlay(t)
	x.fakeB.setTrickplay(true, nil)
	s, body := startOnA(t, x)
	if s.Trickplay != nil || strings.Contains(body, "trickplay") {
		t.Fatalf("previews from a copy without: %s", body)
	}
	if code, _ := getAs(t, x.srvA, subject, "/api/v1/playback/"+s.ID+"/trickplay/0"); code != http.StatusNotFound {
		t.Errorf("a sheet of none: %d", code)
	}
	// With previews, they come.
	x.fakeB.setTrickplay(false, nil)
	if s, body = startOnA(t, x); s.Trickplay == nil || len(s.Trickplay.Sheets) != 7 {
		t.Fatalf("previews: %s", body)
	}
	// Sheets wider than 4,096 pixels, or more than 1,000 of them: none.
	for name, set := range map[string]map[string]int{
		"wide":  {"Width": 1000, "Height": 100, "TileWidth": 10, "TileHeight": 10, "ThumbnailCount": 645, "Interval": 10000},
		"many":  {"Width": 320, "Height": 136, "TileWidth": 1, "TileHeight": 1, "ThumbnailCount": 1001, "Interval": 10000},
		"right": {"Width": 320, "Height": 136, "TileWidth": 1, "TileHeight": 1, "ThumbnailCount": 1000, "Interval": 10000},
	} {
		x.fakeB.setTrickplay(false, set)
		s, body = startOnA(t, x)
		if got := s.Trickplay != nil; got != (name == "right") {
			t.Errorf("%s: %s", name, body)
		}
	}
}
