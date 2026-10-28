package store

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// Last played: a film by when it was played (through any copy), a series
// and a season by their latest episode, an episode by its own; what was
// never played comes last, whichever the order.
func TestLastPlayedOrder(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, prof, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	n := func(i int) *int { return &i }
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "movie", Name: "A", SortName: "a", TMDB: "1", LibraryID: "films"},
		{ID: id(1), Kind: "movie", Name: "A", SortName: "a", TMDB: "1", LibraryID: "4k"},
		{ID: id(2), Kind: "movie", Name: "B", SortName: "b", TMDB: "2", LibraryID: "films"},
		{ID: id(3), Kind: "series", Name: "S", SortName: "s", TMDB: "3", LibraryID: "tv"},
		{ID: id(4), Kind: "season", Name: "S1", SortName: "s1", SeriesID: id(3), SeasonNumber: n(1), LibraryID: "tv"},
		{ID: id(5), Kind: "episode", Name: "E1", SortName: "e1", SeriesID: id(3), SeasonID: id(4), SeasonNumber: n(1), EpisodeNumber: n(1), LibraryID: "tv"},
		{ID: id(6), Kind: "episode", Name: "E2", SortName: "e2", SeriesID: id(3), SeasonID: id(4), SeasonNumber: n(1), EpisodeNumber: n(2), LibraryID: "tv"},
		{ID: id(7), Kind: "season", Name: "S2", SortName: "s2", SeriesID: id(3), SeasonNumber: n(2), LibraryID: "tv"},
		{ID: id(8), Kind: "episode", Name: "E1", SortName: "e1", SeriesID: id(3), SeasonID: id(7), SeasonNumber: n(2), EpisodeNumber: n(1), LibraryID: "tv"},
		{ID: id(9), Kind: "movie", Name: "C", SortName: "c", TMDB: "4", LibraryID: "films"},
	}, 1)
	if err := s.RefreshTitleKeys(ctx); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1_000_000, 0)
	play := func(key, item string, d time.Duration) {
		if err := s.RecordPlay(ctx, prof.ID, key, item, 0, true, at.Add(d)); err != nil {
			t.Fatal(err)
		}
	}
	play("movie/tmdb/4", id(9), 0)
	play("movie/tmdb/1", id(1), time.Hour) // through the 4K copy
	play("series/tmdb/3/s2/e1", id(8), 2*time.Hour)
	play("series/tmdb/3/s1/e1", id(5), 3*time.Hour)

	for _, tc := range []struct {
		name string
		q    ListQuery
		want []string
	}{
		{"films and series", ListQuery{Kinds: []string{"movie", "series"}, Descending: true}, []string{id(3), id(0), id(9), id(2)}},
		{"films, oldest first", ListQuery{Kinds: []string{"movie"}}, []string{id(9), id(0), id(2)}},
		{"seasons", ListQuery{ParentID: id(3), Kinds: []string{"season"}, Descending: true}, []string{id(4), id(7)}},
		{"episodes", ListQuery{ParentID: id(4), Kinds: []string{"episode"}, Descending: true}, []string{id(5), id(6)}},
	} {
		tc.q.Sort, tc.q.ProfileID, tc.q.Limit = "lastPlayed", prof.ID, 10
		got, err := s.ListCatalog(ctx, tc.q)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
