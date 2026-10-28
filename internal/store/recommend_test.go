package store

import (
	"context"
	"fmt"
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
