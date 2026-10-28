package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

// Similar titles: TMDB's first, then the library's titles with most in
// common, the film's own collection aside, as the profile may see them.
func TestSimilarFilledWithRelated(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{store: &st})
	ctx := context.Background()
	now := time.Now()
	lib := fmt.Sprintf("%032x", 0xf1)
	director := fmt.Sprintf("%032x", 0xd1)
	films := []struct {
		tmdb, name, rating string
		people             []string
	}{
		{"1", "Heat", "R", []string{director}},
		{"2", "Collateral", "R", []string{director}}, // TMDB's
		{"3", "Thief", "R", []string{director}},      // related: same director
		{"4", "Miami Vice", "R", []string{director}}, // related, but Heat's collection (say)
		{"5", "Kids heist", "G", []string{director}}, // related, for kids
		{"6", "Unrelated", "R", []string{fmt.Sprintf("%032x", 0xd2)}},
		{"7", "Unrelated too", "R", []string{fmt.Sprintf("%032x", 0xd3)}},
	}
	// Others, so the director is as rare as in a library.
	for i := 8; i <= 16; i++ {
		films = append(films, struct {
			tmdb, name, rating string
			people             []string
		}{fmt.Sprint(i), fmt.Sprint("Other ", i), "R", []string{fmt.Sprintf("%032x", 0xe00+i)}})
	}
	var items []store.CatalogItem
	for i, f := range films {
		items = append(items, store.CatalogItem{ID: fmt.Sprintf("%032x", i+1), Kind: "movie", Name: f.name, SortName: f.name,
			TMDB: f.tmdb, LibraryID: lib, People: f.people})
	}
	st.PutCatalog(ctx, items, 1)
	st.RefreshTitleKeys(ctx)
	st.SetCatalogState(ctx, store.CatalogState{Pass: 1})
	for _, f := range films {
		details := `{}`
		if f.tmdb == "1" {
			details = `{"similar":["2"]}`
		}
		collection := 0
		if f.tmdb == "1" || f.tmdb == "4" {
			collection = 10
		}
		st.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: f.tmdb, Name: f.name, Year: 2000, Collection: collection}, []byte(details), now)
		st.SetTitleRatings(ctx, store.RatedTitle{Kind: "movie", TMDB: f.tmdb}, map[string][]string{"US": {f.rating}}, "2000-01-01", "2000-01-01", now)
	}
	st.SetCollection(ctx, store.Collection{ID: 10, Name: "Saga", Parts: []string{"1", "4"}}, nil, now)

	names := func() []string {
		t.Helper()
		var page struct{ Items []struct{ Name string } }
		if code := get(t, srv, "/api/v1/titles/title_movie_tmdb_1/similar", &page); code != 200 {
			t.Fatalf("similar: %d", code)
		}
		var out []string
		for _, it := range page.Items {
			out = append(out, it.Name)
		}
		return out
	}
	got := names()
	if len(got) == 0 || got[0] != "Collateral" {
		t.Fatalf("TMDB's first: %v", got)
	}
	rest := slices.Clone(got[1:])
	slices.Sort(rest)
	if !slices.Equal(rest, []string{"Kids heist", "Thief"}) {
		t.Errorf("filled with: %v", got)
	}

	// A kids profile: only what's rated for it.
	code, body := post(t, srv, "/api/v1/profiles", `{"name":"Kid","kind":"kids","maxAge":6}`)
	if code != 201 {
		t.Fatalf("kids profile: %d %s", code, body)
	}
	var kid struct{ ID string }
	json.Unmarshal([]byte(body), &kid)
	post(t, srv, "/api/v1/profiles/"+kid.ID+"/activate", `{}`)
	st.SetTitleRatings(ctx, store.RatedTitle{Kind: "movie", TMDB: "1"}, map[string][]string{"US": {"G"}}, "2000-01-01", "2000-01-01", now)
	if got := names(); !slices.Equal(got, []string{"Kids heist"}) {
		t.Errorf("kids profile: %v", got)
	}
}
