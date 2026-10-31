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
	"testing"
	"time"

	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// The titles showing their name instead of a logo (migration 068 lists
// them) are checked again, once: on a French and English server, one with
// only an English logo gets it; one with only its original language's
// (Korean) gets that; one with none keeps its name.
func TestLogosForTitlesWithout(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "t.db")
	box, _ := seal.New(bytes.Repeat([]byte{9}, 32))
	st, err := store.Open(ctx, path, box)
	if err != nil {
		t.Fatal(err)
	}
	st.SetLanguages(ctx, []string{"fr-CA", "en-CA"}, "fr-CA")
	st.SetTMDBLanguage(ctx, "fr-CA")
	for _, id := range []string{"600", "601", "602"} {
		target := store.ArtworkTarget{Kind: "movie", TMDB: id}
		images := map[string]map[string]string{target.Key(): {"poster": "/poster1234.jpg"}}
		if err := st.SetArtwork(ctx, target, images, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	// Migration 068 again, now that there are titles to list.
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE title_history; DROP TABLE title_edits; DROP TABLE title_uploads; PRAGMA user_version = 67`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if st, err = store.Open(ctx, path, box); err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang := r.URL.Query().Get("include_image_language")
		switch {
		case r.URL.Path == "/3/movie/600/images" && lang == "fr,null,en":
			io.WriteString(w, `{"id":600,"logos":[{"file_path":"/onlyeng123.png","iso_639_1":"en"}]}`)
		case r.URL.Path == "/3/movie/601/images" && lang == "fr,null,en", r.URL.Path == "/3/movie/602/images" && lang == "fr,null,en":
			io.WriteString(w, `{"id":`+r.URL.Path[len("/3/movie/"):len("/3/movie/")+3]+`,"logos":[]}`)
		case r.URL.Path == "/3/movie/601":
			io.WriteString(w, `{"id":601,"original_language":"ko"}`)
		case r.URL.Path == "/3/movie/601/images" && lang == "ko":
			io.WriteString(w, `{"id":601,"logos":[{"file_path":"/korean1234.png","iso_639_1":"ko"}]}`)
		case r.URL.Path == "/3/movie/602":
			io.WriteString(w, `{"id":602,"original_language":"en"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	client, err := tmdb.New(srv.URL+"/3", "eyJhbGciOiJIUzI1NiJ9.eyJ0ZXN0Ijp0cnVlfQ.test-signature-for-overfin")
	if err != nil {
		t.Fatal(err)
	}
	client.Languages = func() []string { return []string{"fr-CA", "en-CA"} }
	logos := &Logos{Store: st, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Source: func(language string) LogoSource {
		c := *client
		c.Language = func() string { return language }
		return &c
	}}
	if done, err := logos.Once(ctx); err != nil || !done {
		t.Fatalf("pass: %v %v", done, err)
	}
	art, err := st.Artwork(ctx, []string{"movie/tmdb/600", "movie/tmdb/601", "movie/tmdb/602"})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"movie/tmdb/600": "/onlyeng123.png",
		"movie/tmdb/601": "/korean1234.png",
		"movie/tmdb/602": "",
	} {
		if got := art[key]["logo"]; got != want {
			t.Errorf("%s: logo %q, want %q", key, got, want)
		}
	}
}
