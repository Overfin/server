package store

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// Categories pick the library's films and series by their rules, and leave
// out the titles of the categories they except.
func TestCategories(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	s.SetRatingScales(ctx, "movie", map[string][]RatingLevel{"US": {{"G", "", 1}, {"PG", "", 2}, {"R", "", 4}}})
	s.SetRatingScales(ctx, "series", map[string][]RatingLevel{"US": {{"TV-Y", "", 1}, {"TV-14", "", 4}}})
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "movie", Name: "Heat", SortName: "a", TMDB: "1", LibraryID: "films", Tags: []string{"Classique"}},
		{ID: id(1), Kind: "movie", Name: "Totoro", SortName: "b", TMDB: "2", LibraryID: "films"},
		{ID: id(2), Kind: "series", Name: "Bluey", SortName: "c", TMDB: "3", LibraryID: "series"},
		{ID: id(3), Kind: "series", Name: "Frieren", SortName: "d", TMDB: "4", LibraryID: "anime"},
		{ID: id(4), Kind: "series", Name: "The Wire", SortName: "e", TMDB: "5", LibraryID: "series"},
	}, 1)
	for _, d := range []struct {
		kind, tmdb, lang string
		genres           []TitleGenre
		rating           string
	}{
		{"movie", "1", "en", []TitleGenre{{80, "Crime"}}, "R"},
		{"movie", "2", "ja", []TitleGenre{{16, "Animation"}, {10751, "Family"}}, "G"},
		{"series", "3", "en", []TitleGenre{{16, "Animation"}, {10762, "Kids"}}, "TV-Y"},
		{"series", "4", "ja", []TitleGenre{{16, "Animation"}}, "TV-14"},
		{"series", "5", "en", []TitleGenre{{80, "Crime"}}, "TV-14"},
	} {
		s.SetTitleDetails(ctx, Title{Kind: d.kind, TMDB: d.tmdb, Name: d.tmdb, Genres: d.genres}, []byte(`{"language":"`+d.lang+`"}`), now)
		s.SetTitleRatings(ctx, RatedTitle{d.kind, d.tmdb}, map[string][]string{"US": {d.rating}}, "", "", now)
	}
	save := func(names string, r CategoryRules) *Category {
		c := &Category{Names: map[string]string{"en": names}, Rules: r}
		if err := s.SaveCategory(ctx, c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	anime := save("Anime", CategoryRules{Genres: []int{16}, Languages: []string{"ja"}})
	series := save("Series", CategoryRules{Kinds: []string{"series"}, Except: []string{anime.ID}})
	kids := save("Kids", CategoryRules{MovieCeiling: "PG", SeriesCeiling: "TV-Y"})
	films := save("Films", CategoryRules{Kinds: []string{"movie"}})
	classics := save("Classics", CategoryRules{Tags: []string{"classique"}, Libraries: []string{"films"}})
	everything := save("All", CategoryRules{})

	in := func(c *Category) []string {
		f, err := s.CategoryFilterOf(ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.ListCatalog(ctx, ListQuery{Sort: "name", Category: f, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	for _, tc := range []struct {
		c    *Category
		want []string
	}{
		{anime, []string{id(1), id(3)}},
		{series, []string{id(2), id(4)}},
		{kids, []string{id(1), id(2)}},
		{films, []string{id(0), id(1)}},
		{classics, []string{id(0)}},
		{everything, []string{id(0), id(1), id(2), id(3), id(4)}},
	} {
		if got := in(tc.c); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.c.Names["en"], got, tc.want)
		}
	}
	// With the profile's own limits too.
	f, _ := s.CategoryFilterOf(ctx, anime.ID)
	if n, err := s.CountCatalog(ctx, ListQuery{Category: f, Access: &Access{Libraries: []string{"films"}}}); err != nil || n != 1 {
		t.Errorf("anime in films only: %d %v", n, err)
	}

	// Ordered, renamed, deleted: an excepted category deleted is no longer
	// left out.
	all, _ := s.Categories(ctx)
	if len(all) != 6 || all[0].ID != anime.ID || all[5].ID != everything.ID {
		t.Errorf("order: %v", all)
	}
	order := []string{everything.ID, films.ID, series.ID, anime.ID, kids.ID, classics.ID}
	if err := s.OrderCategories(ctx, order); err != nil {
		t.Fatal(err)
	}
	if err := s.OrderCategories(ctx, order[:2]); err == nil {
		t.Error("a partial order taken")
	}
	all, _ = s.Categories(ctx)
	for i, c := range all {
		if c.ID != order[i] {
			t.Errorf("after ordering: %d is %s", i, c.Names["en"])
		}
	}
	if err := s.DeleteCategory(ctx, anime.ID); err != nil {
		t.Fatal(err)
	}
	if got := in(series); !slices.Equal(got, []string{id(2), id(3), id(4)}) {
		t.Errorf("series once anime is gone: %v", got)
	}
	if s, _ := s.CategoryByID(ctx, series.ID); len(s.Rules.Except) != 0 {
		t.Errorf("except kept: %v", s.Rules.Except)
	}
	if _, err := s.CategoryByID(ctx, anime.ID); err != ErrNotFound {
		t.Errorf("deleted: %v", err)
	}

	// Names, by language.
	c := &Category{Names: map[string]string{"en": "Kids", "fr": "Enfants"}}
	if c.Name("fr-CA") != "Enfants" || c.Name("de") != "Kids" || (&Category{Names: map[string]string{"fr": "Enfants"}}).Name("de") != "Enfants" {
		t.Error("names")
	}

	// Libraries in the menu until the operator says otherwise.
	if on, _ := s.LibrariesShown(ctx); !on {
		t.Error("libraries hidden at first")
	}
	s.SetLibrariesShown(ctx, false)
	if on, _ := s.LibrariesShown(ctx); on {
		t.Error("libraries still shown")
	}
}
