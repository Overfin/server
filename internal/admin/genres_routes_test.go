package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/overfin/server/internal/tmdb"
)

// The genres left out, from the panel: TMDB's of each kind, those left
// out marked; one TMDB no longer lists still shown by number, to take it
// back; numbers that can't be a genre, or a body that isn't one, refused,
// nothing saved; without TMDB itself, unavailable; TMDB failing, said so.
func TestGenresLeftOutRoutes(t *testing.T) {
	p, b, db, _ := signedInPanel(t)
	failing := false
	p.TMDBGenres = func(_ context.Context, kind string) ([]tmdb.Genre, error) {
		if failing {
			return nil, errors.New("tmdb: 500")
		}
		if kind == "movie" {
			return []tmdb.Genre{{ID: 27, Name: "Horror"}, {ID: 35, Name: "Comedy"}}, nil
		}
		return []tmdb.Genre{{ID: 10762, Name: "Kids"}}, nil
	}
	b.h = p.Handler()
	var got genresJSON
	read := func(rec interface{ Result() *http.Response }) {
		t.Helper()
		got = genresJSON{}
		json.NewDecoder(rec.Result().Body).Decode(&got)
	}
	rec := b.do("GET", "/admin/api/genres-left-out", "", nil)
	if read(rec); rec.Code != 200 || len(got.Movie) != 2 || got.Movie[0].Left || len(got.Series) != 1 {
		t.Fatalf("before: %d %+v", rec.Code, got)
	}
	rec = b.do("PUT", "/admin/api/genres-left-out", `{"movie":[27,53],"series":[10762]}`, nil)
	if read(rec); rec.Code != 200 {
		t.Fatalf("set: %d", rec.Code)
	}
	want := genresJSON{Movie: []genreJSON{{27, "Horror", true}, {35, "Comedy", false}, {53, "Genre 53", true}},
		Series: []genreJSON{{10762, "Kids", true}}}
	if b, _ := json.Marshal(got); string(b) != mustJSON(want) {
		t.Errorf("after: %s", b)
	}
	if left, _ := db.GenresLeftOut(context.Background()); len(left.Movie) != 2 || left.Series[0] != 10762 {
		t.Errorf("saved: %+v", left)
	}
	for _, body := range []string{`{"movie":[0]}`, `{"series":[1000001]}`, `{"movie":[1],"other":[]}`, `not json`} {
		if rec := b.do("PUT", "/admin/api/genres-left-out", body, nil); rec.Code != 400 {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
	if left, _ := db.GenresLeftOut(context.Background()); len(left.Movie) != 2 {
		t.Errorf("a refused change saved: %+v", left)
	}
	failing = true
	if rec := b.do("GET", "/admin/api/genres-left-out", "", nil); rec.Code != http.StatusBadGateway {
		t.Errorf("TMDB failing: %d", rec.Code)
	}
	p.TMDBGenres = nil
	b.h = p.Handler()
	if rec := b.do("GET", "/admin/api/genres-left-out", "", nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("without TMDB: %d", rec.Code)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
