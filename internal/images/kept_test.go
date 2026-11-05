package images

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// sizedJPEG is a JPEG w pixels wide, h high.
func sizedJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// fakeImages is TMDB's image server: each path at "original" (and any
// size), counting what's asked.
type fakeImages struct {
	mu    sync.Mutex
	asked []string
	serve func(size, path string) []byte // nil: 404
}

func (f *fakeImages) start(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.asked = append(f.asked, r.URL.Path)
		f.mu.Unlock()
		size, path, _ := cut(r.URL.Path[1:])
		if data := f.serve(size, "/"+path); data != nil {
			w.Write(data)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	old := tmdbImageBase
	tmdbImageBase = srv.URL + "/"
	t.Cleanup(func() { tmdbImageBase = old })
}

func (f *fakeImages) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.asked)
}

func cut(s string) (string, string, bool) {
	for i := range s {
		if s[i] == '/' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

func width(t *testing.T, data []byte) int {
	t.Helper()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Width
}

// Library artwork: its original kept on disk (TMDB's, no wider than the
// largest size served), and every size narrower made from it, kept beside
// it; the original itself the sizes it's no wider than (copies of it
// deleted); served from disk after; a size gone
// made again when asked for; fetched again when old;
// deleted with what was made of it once unwanted, and an older layout's
// files too.
func TestKeep(t *testing.T) {
	big := sizedJPEG(t, 2600, 3900) // a poster larger than any size served
	f := &fakeImages{serve: func(size, path string) []byte {
		if path == "/missing1234.jpg" {
			return nil
		}
		return big
	}}
	f.start(t)
	dir := t.TempDir()
	// An older layout's file: TMDB's own size names.
	os.MkdirAll(filepath.Join(dir, "poster", "w185"), 0o700)
	os.WriteFile(filepath.Join(dir, "poster", "w185", "poster1234.jpg"), []byte("old"), 0o600)
	p := New(nil, 1<<20)
	p.ArtworkDir = dir
	ctx := context.Background()
	poster := TMDBRef{Type: "poster", Path: "/poster1234.jpg"}
	still := TMDBRef{Type: "still", Path: "/still12345.jpg"}
	wanted := []TMDBRef{poster, still, poster, {Type: "still", Path: "/missing1234.jpg"}, {Type: "poster", Path: "../../etc"}}

	fetched, made, deleted, err := p.Keep(ctx, wanted, time.Now().Add(-time.Hour))
	if err != nil || fetched != 2 || made != 6 || deleted != 1 {
		t.Fatalf("first pass: %d %d %d %v", fetched, made, deleted, err)
	}
	has := func(typ, size, name string) bool {
		_, err := os.Stat(filepath.Join(dir, typ, size, name))
		return err == nil
	}
	if has("poster", "2000", "poster1234.jpg") || has("still", "1280", "still12345.jpg") || !has("poster", "1200", "poster1234.jpg") {
		t.Error("a size the original is kept apart, or one narrower isn't")
	}
	original, err := os.ReadFile(filepath.Join(dir, "poster", "original", "poster1234.jpg"))
	if err != nil || width(t, original) != 2000 {
		t.Fatalf("poster's original: %v", err)
	}
	if s, err := os.ReadFile(filepath.Join(dir, "still", "original", "still12345.jpg")); err != nil || width(t, s) != 1280 {
		t.Errorf("still's original: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "poster", "w185")); err == nil {
		t.Error("an older layout's folder kept")
	}

	// A size: the original for the widest, made again when gone; TMDB
	// not asked.
	n := f.count()
	ok := func(context.Context) error { return nil }
	os.Remove(filepath.Join(dir, "poster", "400", "poster1234.jpg"))
	if data, _, err := p.TMDB(ctx, poster, 2000, ok); err != nil || !bytes.Equal(data, original) || has("poster", "2000", "poster1234.jpg") {
		t.Errorf("2000 isn't the original: %v", err)
	}
	data, ct, err := p.TMDB(ctx, poster, 400, ok)
	if err != nil || ct != "image/jpeg" || width(t, data) != 400 {
		t.Fatalf("400: %v", err)
	}
	if f.count() != n {
		t.Error("asked TMDB for a kept image")
	}
	if made, err := os.ReadFile(filepath.Join(dir, "poster", "400", "poster1234.jpg")); err != nil || !bytes.Equal(made, data) {
		t.Errorf("size not kept: %v", err)
	}
	if _, _, err := p.TMDB(ctx, poster, 300, ok); !errors.Is(err, ErrInvalid) {
		t.Errorf("a width not served: %v", err)
	}
	if _, _, err := p.TMDB(ctx, still, 320, func(context.Context) error { return ErrHidden }); err != ErrHidden {
		t.Errorf("check skipped: %v", err)
	}

	// Fresh: nothing fetched again. Old: fetched again, sizes left.
	// A copy of the original (an older Overfin's): deleted.
	copied, _ := os.ReadFile(filepath.Join(dir, "still", "original", "still12345.jpg"))
	os.MkdirAll(filepath.Join(dir, "still", "1280"), 0o700)
	os.WriteFile(filepath.Join(dir, "still", "1280", "still12345.jpg"), copied, 0o600)
	if fetched, made, deleted, _ := p.Keep(ctx, wanted, time.Now().Add(-time.Hour)); fetched != 0 || made != 0 || deleted != 1 {
		t.Errorf("fresh: %d fetched, %d made, %d deleted", fetched, made, deleted)
	}
	if fetched, made, _, _ := p.Keep(ctx, wanted, time.Now().Add(time.Hour)); fetched != 2 || made != 0 {
		t.Errorf("old ones: %d %d", fetched, made)
	}

	// The poster's title left the libraries: its original and its size
	// go, the still stays; nothing wanted at all deletes nothing.
	os.WriteFile(filepath.Join(dir, "still", "original", ".new-123"), []byte("half"), 0o600)
	if _, _, deleted, err := p.Keep(ctx, []TMDBRef{still}, time.Now().Add(-time.Hour)); err != nil || deleted != 5 {
		t.Errorf("unwanted: %d %v", deleted, err)
	}
	for _, gone := range []string{"poster/original/poster1234.jpg", "poster/400/poster1234.jpg", "still/original/.new-123"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("kept: %s", gone)
		}
	}
	if _, _, deleted, _ := p.Keep(ctx, nil, time.Now().Add(-time.Hour)); deleted != 0 {
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

// An image no library has: its original and sizes in memory only; a
// size wider than the original is the original.
func TestTMDBNotKept(t *testing.T) {
	f := &fakeImages{serve: func(size, path string) []byte { return sizedJPEG(t, 900, 500) }}
	f.start(t)
	dir := t.TempDir()
	p := New(nil, 1<<20)
	p.ArtworkDir = dir
	ctx := context.Background()
	ok := func(context.Context) error { return nil }
	backdrop := TMDBRef{Type: "backdrop", Path: "/backdrop12.jpg"}
	if data, _, err := p.TMDB(ctx, backdrop, 320, ok); err != nil || width(t, data) != 320 {
		t.Fatalf("320: %v", err)
	}
	if data, _, err := p.TMDB(ctx, backdrop, 1920, ok); err != nil || width(t, data) != 900 {
		t.Fatalf("wider than the original: %v", err)
	}
	if f.count() != 1 {
		t.Errorf("TMDB asked %d times", f.count())
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("kept on disk: %v", left)
	}
}

// An original Overfin doesn't take (WebP whatever its name says, or over
// MaxDimension) gives way to TMDB's largest ready-made size; nothing that
// isn't JPEG or PNG is ever served.
func TestTMDBOriginalFallback(t *testing.T) {
	webp := []byte("RIFF\x24\x00\x00\x00WEBPVP8 ")
	f := &fakeImages{serve: func(size, path string) []byte {
		switch {
		case size == "original":
			return webp
		case size == "w780" && path == "/poster1234.jpg":
			return sizedJPEG(t, 780, 1170)
		}
		return nil
	}}
	f.start(t)
	p := New(nil, 1<<20)
	ctx := context.Background()
	ok := func(context.Context) error { return nil }
	if data, _, err := p.TMDB(ctx, TMDBRef{Type: "poster", Path: "/poster1234.jpg"}, 400, ok); err != nil || width(t, data) != 400 {
		t.Errorf("poster: %v", err)
	}
	if _, _, err := p.TMDB(ctx, TMDBRef{Type: "logo", Path: "/logo123456.png"}, 250, ok); err == nil {
		t.Error("a logo with no image Overfin takes was served")
	}
}

// Widths asked are served as the next one made, the largest beyond; size
// names stand for fixed ones.
func TestWidths(t *testing.T) {
	for _, tc := range []struct {
		typ         string
		asked, want int
		ok          bool
	}{
		{"poster", 1, 200, true}, {"poster", 200, 200, true}, {"poster", 201, 400, true}, {"poster", 5000, 2000, true},
		{"backdrop", 700, 1280, true}, {"still", 4000, 1280, true}, {"poster", 0, 0, false}, {"trailer", 100, 0, false},
	} {
		if got, ok := Width(tc.typ, tc.asked); got != tc.want || ok != tc.ok {
			t.Errorf("%s %d: %d %v", tc.typ, tc.asked, got, ok)
		}
	}
	for typ, sizes := range named {
		for _, w := range sizes {
			if got, _ := Width(typ, w); got != w {
				t.Errorf("%s: %d isn't served", typ, w)
			}
		}
	}
	if Smallest("backdrop") != 320 || Smallest("nothing") != 0 {
		t.Error("smallest")
	}
}
