package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// What the valuation reads: who counts, what they watched, marked and
// asked for, and the films and seasons the libraries have, with their
// lengths; a disabled account doesn't count.
func TestValuationFacts(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	acct, prof, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	gone, goneProf, _ := s.CreateAccount(ctx, "subject-2", owner("2"))
	if err := s.SetAccountDisabled(ctx, gone.ID, true); err != nil {
		t.Fatal(err)
	}
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	n := func(i int) *int { return &i }
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "movie", Name: "Film", SortName: "film", TMDB: "7", RuntimeMs: 6_000_000, AddedAt: now},
		{ID: id(1), Kind: "series", Name: "Show", SortName: "show", TMDB: "8"},
		{ID: id(2), Kind: "episode", Name: "E1", SortName: "e1", SeriesID: id(1), SeasonNumber: n(1), EpisodeNumber: n(1), RuntimeMs: 1_200_000, AddedAt: now},
	}, 1)
	s.RefreshTitleKeys(ctx)
	s.RecordPlay(ctx, prof.ID, "movie/tmdb/7", id(0), 3_000_000, false, now)
	s.RecordPlay(ctx, goneProf.ID, "series/tmdb/8/s1/e1", id(2), 0, true, now)
	s.SetFavorite(ctx, prof.ID, "series/tmdb/8", id(1), true, now)
	if err := s.AddRequest(ctx, &Request{AccountID: acct.ID, ProfileID: prof.ID, Title: "series/tmdb/8", Seasons: []int{2},
		Status: "pending", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	f, err := s.ValuationFacts(ctx, now, []ProfileTitle{{ProfileID: prof.ID, Title: "movie/tmdb/9"}})
	if err != nil {
		t.Fatal(err)
	}
	if !f.Profiles[prof.ID].Active || f.Profiles[goneProf.ID].Active {
		t.Errorf("profiles: %+v", f.Profiles)
	}
	film, s1, s2, wanted := f.Titles["movie/tmdb/7"], f.Titles["series/tmdb/8/s1"], f.Titles["series/tmdb/8/s2"], f.Titles["movie/tmdb/9"]
	if film == nil || !film.InLibrary || film.RuntimeMs != 6_000_000 || film.Name != "Film" {
		t.Errorf("the film: %+v", film)
	}
	if s1 == nil || !s1.InLibrary || s1.Season != 1 || s1.Episodes[1] != 1_200_000 || s1.Name != "Show" {
		t.Errorf("season 1: %+v", s1)
	}
	if film.Added.IsZero() || s1.Added.IsZero() {
		t.Errorf("arrival: the film %v, season 1 %v", film.Added, s1.Added)
	}
	if s2 == nil || s2.InLibrary {
		t.Errorf("season 2, asked for: %+v", s2)
	}
	if wanted == nil || wanted.InLibrary {
		t.Errorf("a film on a watchlist: %+v", wanted)
	}
	if len(f.Viewings) != 2 || len(f.Favorites) != 1 || len(f.Requests) != 1 || f.Requests[0].Title != "series/tmdb/8/s2" {
		t.Errorf("viewings %+v, favorites %+v, requests %+v", f.Viewings, f.Favorites, f.Requests)
	}
}

// The operator's weights are kept, and nil goes back to none.
func TestValuationWeights(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if w, err := s.ValuationWeights(ctx); w != nil || err != nil {
		t.Fatalf("never set: %v %v", w, err)
	}
	if err := s.SetValuationWeights(ctx, map[string]int{"rating": 3}); err != nil {
		t.Fatal(err)
	}
	if w, _ := s.ValuationWeights(ctx); w["rating"] != 3 {
		t.Errorf("set: %v", w)
	}
	if err := s.SetValuationWeights(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if w, _ := s.ValuationWeights(ctx); w != nil {
		t.Errorf("reset: %v", w)
	}
}
