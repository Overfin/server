package images

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// Library artwork kept on disk: fetched once at the kept sizes, served
// from disk after, fetched again when old, deleted once unwanted.
func TestKeep(t *testing.T) {
	jpg := sampleJPEG(t)
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		if r.URL.Path == "/w185/missing1234.jpg" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(jpg)
	}))
	defer srv.Close()
	old := tmdbImageBase
	tmdbImageBase = srv.URL + "/"
	defer func() { tmdbImageBase = old }()

	dir := t.TempDir()
	p := New(nil, 1<<20)
	p.ArtworkDir = dir
	ctx := context.Background()
	poster := TMDBRef{Type: "poster", Path: "/poster1234.jpg"}
	still := TMDBRef{Type: "still", Path: "/still12345.jpg"}
	wanted := []TMDBRef{poster, still, poster, {Type: "still", Path: "/missing1234.jpg"}, {Type: "poster", Path: "../../etc"}}

	fetched, deleted, err := p.Keep(ctx, wanted, time.Now().Add(-time.Hour))
	if err != nil || fetched != 3 || deleted != 0 {
		t.Fatalf("first pass: %d %d %v", fetched, deleted, err)
	}
	for _, f := range []string{"poster/w185/poster1234.jpg", "poster/w342/poster1234.jpg", "still/w185/still12345.jpg"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("not kept: %s", f)
		}
	}

	// Served from disk: TMDB isn't asked. The check still runs.
	n := asked.Load()
	if data, ct, err := p.TMDB(ctx, poster, "medium", func(context.Context) error { return nil }); err != nil || ct != "image/jpeg" || len(data) == 0 {
		t.Fatalf("served: %v", err)
	}
	if asked.Load() != n {
		t.Error("asked TMDB for a kept image")
	}
	if _, _, err := p.TMDB(ctx, still, "small", func(context.Context) error { return ErrHidden }); err != ErrHidden {
		t.Errorf("check skipped: %v", err)
	}

	// Fresh: nothing fetched again. Old: fetched again.
	if fetched, _, _ := p.Keep(ctx, wanted, time.Now().Add(-time.Hour)); fetched != 0 {
		t.Errorf("fetched again while fresh: %d", fetched)
	}
	if fetched, _, _ := p.Keep(ctx, wanted, time.Now().Add(time.Hour)); fetched != 3 {
		t.Errorf("old ones: %d", fetched)
	}

	// The still's title left the libraries: deleted; nothing wanted at all
	// deletes nothing.
	os.WriteFile(filepath.Join(dir, "poster", "w185", ".new-123"), []byte("half"), 0o600)
	if _, deleted, err := p.Keep(ctx, []TMDBRef{poster}, time.Now().Add(-time.Hour)); err != nil || deleted != 1 {
		t.Errorf("unwanted: %d %v", deleted, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "still/w185/still12345.jpg")); err == nil {
		t.Error("unwanted still kept")
	}
	if _, err := os.Stat(filepath.Join(dir, "poster", "w185", ".new-123")); err == nil {
		t.Error("half-written file kept")
	}
	if _, deleted, _ := p.Keep(ctx, nil, time.Now().Add(-time.Hour)); deleted != 0 {
		t.Errorf("empty wanted deleted %d", deleted)
	}

	// No more TMDB: every kept image goes, and the folder stays.
	if err := p.ForgetKept(); err != nil {
		t.Fatal(err)
	}
	if left, err := os.ReadDir(dir); err != nil || len(left) != 0 {
		t.Errorf("after forgetting: %v %v", left, err)
	}
	if err := (&Pipeline{ArtworkDir: filepath.Join(dir, "none")}).ForgetKept(); err != nil {
		t.Errorf("no folder: %v", err)
	}
}

// TMDB serves some sizes as WebP whatever their name says: the next
// larger one is asked then, once, and nothing that isn't JPEG or PNG is
// ever served.
func TestTMDBWebPSize(t *testing.T) {
	jpg := sampleJPEG(t)
	webp := []byte("RIFF\x24\x00\x00\x00WEBPVP8 ")
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		switch r.URL.Path {
		case "/w500/poster1234.jpg", "/w1280/backdrop12.jpg":
			w.Write(jpg)
		default:
			w.Write(webp)
		}
	}))
	defer srv.Close()
	old := tmdbImageBase
	tmdbImageBase = srv.URL + "/"
	defer func() { tmdbImageBase = old }()
	p := New(nil, 1<<20)
	ok := func(context.Context) error { return nil }
	if data, _, err := p.TMDB(context.Background(), TMDBRef{Type: "poster", Path: "/poster1234.jpg"}, "medium", ok); err != nil || len(data) == 0 {
		t.Errorf("poster: %v", err)
	}
	if want := []string{"/w342/poster1234.jpg", "/w500/poster1234.jpg"}; !slices.Equal(asked, want) {
		t.Errorf("asked %v, want %v", asked, want)
	}
	// The largest already: nothing else to ask.
	asked = nil
	if _, _, err := p.TMDB(context.Background(), TMDBRef{Type: "logo", Path: "/logo123456.png"}, "large", ok); !errors.Is(err, ErrUnsafe) {
		t.Errorf("logo: %v", err)
	}
	if len(asked) != 1 {
		t.Errorf("asked %v", asked)
	}
}
