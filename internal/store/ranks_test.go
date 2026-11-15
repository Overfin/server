package store

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// Ranks are written whole, read by key, sort listings (the unranked last),
// and give the copies of the titles ranked before one, to count those a
// profile may see.
func TestTitleRanks(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "movie", Name: "Arrival", SortName: "a", TMDB: "1", LibraryID: "films"},
		{ID: id(1), Kind: "movie", Name: "Arrival", SortName: "a", TMDB: "1", LibraryID: "films-4k"},
		{ID: id(2), Kind: "movie", Name: "Heat", SortName: "h", TMDB: "2", LibraryID: "films"},
		{ID: id(3), Kind: "movie", Name: "Zorro", SortName: "z", TMDB: "3", LibraryID: "films"},
	}, 1)
	if err := s.RefreshTitleKeys(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTitleRanks(ctx, map[string]TitleRank{"movie/tmdb/9": {"movie", 9}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTitleRanks(ctx, map[string]TitleRank{
		"movie/tmdb/2": {"movie", 1}, "movie/tmdb/1": {"movie", 2}, "series/tmdb/5": {"series", 1},
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.RankOf(ctx, "movie/tmdb/9"); ok {
		t.Error("an old rank kept")
	}
	if r, ok, err := s.RankOf(ctx, "movie/tmdb/1"); err != nil || !ok || r != (TitleRank{"movie", 2}) {
		t.Errorf("rank of 1: %v %v %v", r, ok, err)
	}
	got, err := s.ListCatalog(ctx, ListQuery{Kinds: []string{"movie"}, Sort: "rank", Limit: 10})
	if err != nil || !slices.Equal(got, []string{id(2), id(0), id(3)}) {
		t.Errorf("by rank: %v %v", got, err)
	}
	before, err := s.RankedBefore(ctx, "movie", 2)
	if err != nil || len(before) != 1 || !slices.Equal(before["movie/tmdb/2"], []string{id(2)}) {
		t.Errorf("before rank 2: %v %v", before, err)
	}
	if before, _ := s.RankedBefore(ctx, "movie", 3); len(before["movie/tmdb/1"]) != 2 {
		t.Errorf("both copies of Arrival: %v", before)
	}
}
