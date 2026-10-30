package catalog

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// The logos kept before they were taken only in their language are
// checked again with TMDB, once, on a French server: the English one
// becomes the French one, else the one without text, else none; the other
// language's texts get theirs; a title TMDB fails on stays for later, one
// it doesn't know is taken off; then the list is empty.
func TestLogosCheckedAgain(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "t.db")
	box, _ := seal.New(bytes.Repeat([]byte{9}, 32))
	st, err := store.Open(ctx, path, box)
	if err != nil {
		t.Fatal(err)
	}
	st.SetLanguages(ctx, []string{"fr-CA", "en-CA"}, "fr-CA")
	st.SetTMDBLanguage(ctx, "fr-CA")
	// As the titles' keeper kept them, an English logo where French had
	// none.
	for _, a := range []struct{ kind, tmdb, logo string }{
		{"movie", "550988", "/freeguy123.png"}, // TMDB has a French one now
		{"series", "1399", "/gotenglish.png"},  // one without text
		{"movie", "600", "/onlyeng123.png"},    // only English
		{"movie", "700", "/failing123.png"},    // TMDB fails on it
		{"movie", "404", "/unknown123.png"},    // TMDB doesn't know it
	} {
		target := store.ArtworkTarget{Kind: a.kind, TMDB: a.tmdb}
		images := map[string]map[string]string{target.Key(): {"logo": a.logo, "poster": "/poster1234.jpg"}}
		if err := st.SetArtwork(ctx, target, images, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	// Its English texts were kept without a logo.
	st.SetTitleTexts(ctx, "en-CA", store.Title{Kind: "movie", TMDB: "550988", Name: "Free Guy"}, []byte(`{}`), "", time.Now())
	st.Close()
	// Migration 066 again, now that there are logos to list.
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE logos_to_check; DROP TABLE collections_to_find; DROP TABLE collections;
		DROP INDEX catalog_titles_collection; ALTER TABLE catalog_titles DROP COLUMN collection; DROP TABLE title_history; PRAGMA user_version = 65`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if st, err = store.Open(ctx, path, box); err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var failing atomic.Bool
	failing.Store(true)
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		lang := r.URL.Query().Get("include_image_language")
		switch {
		case r.URL.Path == "/3/movie/550988/images" && lang == "fr,null":
			io.WriteString(w, `{"id":550988,"logos":[{"file_path":"/nolang1234.png","iso_639_1":null},{"file_path":"/hommelibre.png","iso_639_1":"fr"}]}`)
		case r.URL.Path == "/3/movie/550988/images" && lang == "en,null":
			io.WriteString(w, `{"id":550988,"logos":[{"file_path":"/freeguy123.png","iso_639_1":"en"}]}`)
		case r.URL.Path == "/3/tv/1399/images" && lang == "fr,null":
			io.WriteString(w, `{"id":1399,"logos":[{"file_path":"/gotnolang1.png","iso_639_1":null}]}`)
		case r.URL.Path == "/3/movie/600/images" && lang == "fr,null":
			io.WriteString(w, `{"id":600,"logos":[]}`)
		case r.URL.Path == "/3/movie/700/images" && lang == "fr,null" && !failing.Load():
			io.WriteString(w, `{"id":700,"logos":[{"file_path":"/failfixed1.png","iso_639_1":"fr"}]}`)
		case r.URL.Path == "/3/movie/700/images":
			w.WriteHeader(http.StatusBadGateway)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	client, err := tmdb.New(srv.URL+"/3", "eyJhbGciOiJIUzI1NiJ9.eyJ0ZXN0Ijp0cnVlfQ.test-signature-for-overfin")
	if err != nil {
		t.Fatal(err)
	}
	logos := &Logos{Store: st, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Source: func(language string) LogoSource {
		c := *client
		c.Language = func() string { return language }
		return &c
	}}

	done, err := logos.Once(ctx)
	if err != nil || done {
		t.Fatalf("first pass: %v %v", done, err)
	}
	keys := []string{"movie/tmdb/550988", "series/tmdb/1399", "movie/tmdb/600", "movie/tmdb/700", "movie/tmdb/404"}
	art, err := st.Artwork(ctx, keys)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"movie/tmdb/550988": "/hommelibre.png",
		"series/tmdb/1399":  "/gotnolang1.png",
		"movie/tmdb/600":    "",
		"movie/tmdb/700":    "/failing123.png", // kept as it was, for later
		"movie/tmdb/404":    "/unknown123.png", // the titles' keeper's business
	} {
		if got := art[key]["logo"]; got != want {
			t.Errorf("%s: logo %q, want %q", key, got, want)
		}
		if art[key]["poster"] != "/poster1234.jpg" {
			t.Errorf("%s: poster %q", key, art[key]["poster"])
		}
	}
	// In English, the English logo; in French, the French one.
	infos, err := st.TitleInfos(ctx, []string{"movie/tmdb/550988"}, "2026-10-28", "en-CA")
	if err != nil || infos["movie/tmdb/550988"].Logo != "/freeguy123.png" {
		t.Errorf("in English: %+v %v", infos["movie/tmdb/550988"], err)
	}
	infos, err = st.TitleInfos(ctx, []string{"movie/tmdb/550988"}, "2026-10-28", "")
	if err != nil || infos["movie/tmdb/550988"].Logo != "/hommelibre.png" {
		t.Errorf("in French: %+v %v", infos["movie/tmdb/550988"], err)
	}
	// The new logo may be served, the old one no more.
	if keys, _ := st.ImageTitles(ctx, "logo", "/hommelibre.png", 10); len(keys) != 1 {
		t.Errorf("new logo allowed for: %v", keys)
	}
	if keys, _ := st.ImageTitles(ctx, "logo", "/onlyeng123.png", 10); len(keys) != 0 {
		t.Errorf("old logo allowed for: %v", keys)
	}
	todo, _ := st.LogosToCheck(ctx, 10)
	if len(todo) != 1 || todo[0].Key() != "movie/tmdb/700" {
		t.Fatalf("left: %+v", todo)
	}

	// Without TMDB (Seerr only, say), nothing is asked and the title waits.
	source := logos.Source
	logos.Source = func(string) LogoSource { return nil }
	before := asked.Load()
	if done, err := logos.Once(ctx); err != nil || done || asked.Load() != before {
		t.Errorf("without TMDB: %v %v", done, err)
	}
	logos.Source = source
	failing.Store(false)
	if done, err := logos.Once(ctx); err != nil || !done {
		t.Fatalf("second pass: %v %v", done, err)
	}
	art, _ = st.Artwork(ctx, []string{"movie/tmdb/700"})
	if got := art["movie/tmdb/700"]["logo"]; got != "/failfixed1.png" {
		t.Errorf("after failing: %q", got)
	}
	// Nothing left: no request more.
	before = asked.Load()
	if done, err := logos.Once(ctx); err != nil || !done || asked.Load() != before {
		t.Errorf("empty list: %v %v, %d requests", done, err, asked.Load()-before)
	}
}
