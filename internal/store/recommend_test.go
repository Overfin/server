package store

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"
)

// The facts of the items asked for only, each title once: a series as new
// as its newest episode, a film in two libraries by its newest copy.
func TestFactsOfItems(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return fmt.Sprintf("%032x", n) }
	one := 1
	at := func(n int64) time.Time { return time.Unix(1e9+n, 0) }
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(1), Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "949", LibraryID: id(0xf1), AddedAt: at(10)},
		{ID: id(2), Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "949", LibraryID: id(0xf2), AddedAt: at(20)},
		{ID: id(3), Kind: "movie", Name: "Other", SortName: "other", TMDB: "13", LibraryID: id(0xf1), AddedAt: at(5)},
		{ID: id(4), Kind: "series", Name: "Show", SortName: "show", TMDB: "1399", LibraryID: id(0xf1), AddedAt: at(1)},
		{ID: id(5), Kind: "episode", Name: "E1", SortName: "e1", SeriesID: id(4), SeasonNumber: &one, EpisodeNumber: &one, LibraryID: id(0xf1), AddedAt: at(30)},
	}, 1)
	s.RefreshTitleKeys(ctx)
	facts, err := s.FactsOfItems(ctx, []string{id(1), id(2), id(4)})
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 2 {
		t.Fatalf("facts: %v", facts)
	}
	if f := facts["movie/tmdb/949"]; f == nil || !f.Latest.Equal(at(20)) || f.Kind != "movie" {
		t.Errorf("film: %+v", f)
	}
	if f := facts["series/tmdb/1399"]; f == nil || !f.Latest.Equal(at(30)) {
		t.Errorf("series: %+v", f)
	}
	if ids, err := s.CatalogIDs(ctx, ListQuery{Kinds: []string{"movie", "series"}}); err != nil || len(ids) != 4 {
		t.Errorf("ids: %v %v", ids, err)
	}
}

// The library's facts are read once for a while, then again: a title
// added meanwhile, or a new age, shows once they're read again. A caller
// changing its copy changes no one else's.
func TestFactsAreKeptBriefly(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Unix(2e9, 0)
	s.now = func() time.Time { return now }
	id := func(n int) string { return fmt.Sprintf("%032x", n) }
	s.PutCatalog(ctx, []CatalogItem{{ID: id(1), Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "949", LibraryID: "films"}}, 1)
	s.RefreshTitleKeys(ctx)
	facts, err := s.FactsOfItems(ctx, []string{id(1), id(2)})
	if err != nil || len(facts) != 1 || facts["movie/tmdb/949"].Age != nil {
		t.Fatalf("facts: %v, %v", facts, err)
	}
	facts["movie/tmdb/949"].Rating = 99

	s.PutCatalog(ctx, []CatalogItem{{ID: id(2), Kind: "movie", Name: "Other", SortName: "other", TMDB: "13", LibraryID: "films"}}, 1)
	s.RefreshTitleKeys(ctx)
	s.SetTitleRatings(ctx, RatedTitle{Kind: "movie", TMDB: "949"}, map[string][]string{"US": {"PG-13"}}, "", "", now)
	now = now.Add(factsFor - time.Second)
	facts, _ = s.FactsOfItems(ctx, []string{id(1), id(2)})
	if len(facts) != 1 || facts["movie/tmdb/949"].Rating != 0 || facts["movie/tmdb/949"].Age != nil {
		t.Errorf("kept: %v", facts)
	}
	now = now.Add(time.Second)
	facts, _ = s.FactsOfItems(ctx, []string{id(1), id(2)})
	if f := facts["movie/tmdb/949"]; len(facts) != 2 || f.Age == nil || *f.Age != 13 {
		t.Errorf("read again: %v", facts)
	}
}

// What a profile watched weighs its titles' genres, a series through its
// episodes, the recent more than the old; a film it only marked a
// favorite, or a file no one identified, counts for nothing; one without
// genres is seen, but weighs none.
func TestWatchedGenres(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, prof, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	now := time.Unix(2e9, 0)
	for _, d := range []struct {
		kind, tmdb string
		genres     []TitleGenre
	}{
		{"movie", "949", []TitleGenre{{80, "Crime"}, {18, "Drama"}}},
		{"movie", "13", []TitleGenre{{35, "Comedy"}}},
		{"series", "1399", []TitleGenre{{18, "Drama"}, {10765, "Sci-Fi & Fantasy"}}},
	} {
		if err := s.SetTitleDetails(ctx, Title{Kind: d.kind, TMDB: d.tmdb, Name: d.tmdb, Genres: d.genres}, []byte(`{}`), now); err != nil {
			t.Fatal(err)
		}
	}
	s.RecordPlay(ctx, prof.ID, "movie/tmdb/949", "", 0, true, now)                              // now: weighs 1
	s.RecordPlay(ctx, prof.ID, "series/tmdb/1399/s1/e2", "", 60_000, false, now.Add(-halfLife)) // started, a half-life ago
	s.SetFavorite(ctx, prof.ID, "movie/tmdb/13", "", true, now)
	s.RecordPlay(ctx, prof.ID, "item/abc", "abc", 0, true, now)
	// Watched, but without genres (its page not fetched): seen all the same.
	s.RecordPlay(ctx, prof.ID, "movie/tmdb/77", "", 0, true, now)

	weights, seen, err := s.WatchedGenres(ctx, prof.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]float64{80: 1, 18: 1.5, 10765: 0.5}
	if len(weights) != len(want) {
		t.Errorf("weights = %v, want %v", weights, want)
	}
	for g, w := range want {
		if math.Abs(weights[g]-w) > 1e-9 {
			t.Errorf("genre %d weighs %v, want %v", g, weights[g], w)
		}
	}
	if len(seen) != 3 || !seen["movie/tmdb/949"] || !seen["series/tmdb/1399"] || !seen["movie/tmdb/77"] {
		t.Errorf("seen = %v", seen)
	}
}
