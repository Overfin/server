package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/msfp-io/msfp-go/wire"
	"github.com/overfin/server/internal/store"
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

// sheetOnA fetches a sheet through A, as A's viewer.
func sheetOnA(t *testing.T, x *twoServers, who, path string) (int, string) {
	t.Helper()
	return getAs(t, x.srvA, who, path)
}

// A paired server's previews come with its copy's previewLayout in the
// catalog; during a play, A serves them under its own playback's ticket,
// each sheet fetched from B under B's (never ahead) and re-encoded, and
// refuses sheets that aren't within the layout.
func TestPeerPlaybackPreviews(t *testing.T) {
	var standIn sheetStandIn
	x := pairForPlayWith(t, standIn.wrap)
	s := x.playPeer(t)
	sheet := "/api/v1/stream/" + s.Ticket + "/trickplay/0"
	if code, got := sheetOnA(t, x, subject, sheet); code != 200 || !strings.HasPrefix(got, "\xff\xd8") {
		t.Fatalf("the sheet through A: %d", code)
	}
	if code, _ := sheetOnA(t, x, "other-subject", sheet); code != http.StatusNotFound {
		t.Errorf("another account's: %d", code)
	}
	for _, bad := range []string{"1", "00", "-1"} {
		if code, _ := sheetOnA(t, x, subject, "/api/v1/stream/"+s.Ticket+"/trickplay/"+bad); code != http.StatusNotFound {
			t.Errorf("sheet %s: %d", bad, code)
		}
	}
	// What B sends, A re-encodes: nothing but the pixels comes through.
	marker := "a trailer that is not pixels"
	body := append(jpegOf(t, 64, 32), marker...)
	standIn.body.Store(&body)
	s = x.playPeer(t)
	if code, got := sheetOnA(t, x, subject, "/api/v1/stream/"+s.Ticket+"/trickplay/0"); code != 200 || !strings.HasPrefix(got, "\xff\xd8") || strings.Contains(got, marker) {
		t.Fatalf("a re-encoded sheet: %d, marker kept %v", code, strings.Contains(got, marker))
	}
	// Too many bytes, wider or taller than a sheet of its tiles (2 × 160,
	// 2 × 90), not a JPEG: refused.
	var pngSheet bytes.Buffer
	png.Encode(&pngSheet, image.NewRGBA(image.Rect(0, 0, 64, 32)))
	for name, bad := range map[string][]byte{
		"too large": append(jpegOf(t, 64, 32), make([]byte, 9<<20)...),
		"too wide":  jpegOf(t, 321, 8),
		"too tall":  jpegOf(t, 8, 181),
		"a PNG":     pngSheet.Bytes(),
	} {
		standIn.body.Store(&bad)
		s = x.playPeer(t)
		if code, _ := sheetOnA(t, x, subject, "/api/v1/stream/"+s.Ticket+"/trickplay/0"); code != http.StatusNotFound {
			t.Errorf("%s: %d", name, code)
		}
	}
}

// Before a play, a paired server's copy's previews come on demand: A's
// previews ticket (POST /previews, its session and profile only), each
// sheet fetched from B when asked (MSFP's previews call and B's previews
// ticket), kept by the copy's etag and layout; a copy without a
// previewLayout has none.
func TestPeerPreviewsBeforePlay(t *testing.T) {
	ctx := context.Background()
	x := pairForPlay(t)
	var page struct{ Copies []wire.Copy }
	get(t, x.srvA, "/api/v1/titles/title_movie_tmdb_949", &page)
	status, raw := post(t, x.srvA, "/api/v1/previews", `{"titleId":"title_movie_tmdb_949","versionId":"`+page.Copies[0].ID+`"}`)
	var a struct{ Ticket, Etag string }
	json.Unmarshal([]byte(raw), &a)
	if status != 200 || a.Ticket == "" || a.Etag != page.Copies[0].ETag {
		t.Fatalf("previews on A: %d %s", status, raw)
	}
	if n := len(livePlaybacks(x.onB)); n != 0 {
		t.Fatalf("a playback on B for previews: %d", n)
	}
	sheet := "/api/v1/stream/" + a.Ticket + "/trickplay/0"
	if code, got := sheetOnA(t, x, subject, sheet); code != 200 || !strings.HasPrefix(got, "\xff\xd8") {
		t.Fatalf("the sheet through A: %d", code)
	}
	if code, _ := sheetOnA(t, x, "other-subject", sheet); code != http.StatusNotFound {
		t.Errorf("another account's: %d", code)
	}
	if code, _ := sheetOnA(t, x, subject, "/api/v1/stream/"+a.Ticket+"/trickplay/1"); code != http.StatusNotFound {
		t.Errorf("past the layout: %d", code)
	}
	// Up: a copy without previews.
	putCopy(t, x.stB, fmt.Sprintf("%032x", 3), upCopy, 1)
	x.stA.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "14160", Name: "Up", Year: 2009}, []byte(`{}`), time.Now())
	x.a.ReadNow(ctx, x.bOnA.ID)
	get(t, x.srvA, "/api/v1/titles/title_movie_tmdb_14160", &page)
	if len(page.Copies) != 1 {
		t.Fatalf("Up on A: %+v", page.Copies)
	}
	if status, _ := post(t, x.srvA, "/api/v1/previews", `{"titleId":"title_movie_tmdb_14160","versionId":"`+page.Copies[0].ID+`"}`); status != http.StatusNotFound {
		t.Errorf("previews of a copy without: %d", status)
	}
}
