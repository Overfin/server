package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// An item in words, for the control panel's live view.
func TestItemDescription(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	s5, e2 := 5, 2
	s.PutCatalog(ctx, []CatalogItem{
		{ID: strings.Repeat("1", 32), Kind: "movie", Name: "Heat", SortName: "heat", Year: 1995},
		{ID: strings.Repeat("2", 32), Kind: "series", Name: "19-2", SortName: "19-2"},
		{ID: strings.Repeat("3", 32), Kind: "episode", Name: "Le poison", SortName: "le poison", SeriesID: strings.Repeat("2", 32),
			SeasonNumber: &s5, EpisodeNumber: &e2},
	}, 1)
	// TMDB's name of the film, when known, rather than Jellyfin's (a
	// folder's name, tags and all).
	s.PutCatalog(ctx, []CatalogItem{{ID: strings.Repeat("4", 32), Kind: "movie", Name: "Sherlock Jr. (1924) [tmdbid-992]", SortName: "s", TMDB: "992"}}, 1)
	if err := s.PutTitles(ctx, []Title{{Kind: "movie", TMDB: "992", Name: "Sherlock Jr.", Year: 1924}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{strings.Repeat("1", 32): "Heat (1995)", strings.Repeat("4", 32): "Sherlock Jr. (1924)", strings.Repeat("3", 32): "19-2 · S5 E2 · Le poison",
		strings.Repeat("9", 32): ""} {
		if got := s.ItemDescription(ctx, id); got != want {
			t.Errorf("%s: %q, want %q", id[:1], got, want)
		}
	}
}
