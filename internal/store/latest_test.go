package store

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

// A series counts as new when one of its episodes is: "latest" orders it by
// its newest episode, and lists no seasons or episodes of its own.
func TestLatestOrdersSeriesByTheirNewestEpisode(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	day := func(d int) time.Time { return time.Date(2026, 10, d, 12, 0, 0, 0, time.UTC) }
	one, two := 1, 2
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "movie", Name: "Film", SortName: "a", LibraryID: "lib", AddedAt: day(10)},
		{ID: id(1), Kind: "series", Name: "Old show", SortName: "b", LibraryID: "lib", AddedAt: day(1)},
		{ID: id(2), Kind: "season", Name: "Season 1", SortName: "c", SeriesID: id(1), LibraryID: "lib", AddedAt: day(1)},
		{ID: id(3), Kind: "episode", Name: "E1", SortName: "d", SeriesID: id(1), SeasonID: id(2), SeasonNumber: &one, EpisodeNumber: &one, LibraryID: "lib", AddedAt: day(1)},
		{ID: id(4), Kind: "episode", Name: "E2", SortName: "e", SeriesID: id(1), SeasonID: id(2), SeasonNumber: &one, EpisodeNumber: &two, LibraryID: "lib", AddedAt: day(20)},
		{ID: id(5), Kind: "series", Name: "New show", SortName: "f", LibraryID: "lib", AddedAt: day(15)},
	}, 1)
	got, err := s.ListCatalog(ctx, ListQuery{Kinds: []string{"movie", "series"}, Sort: "latest", Descending: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{id(1), id(5), id(0)}; !slices.Equal(got, want) {
		t.Errorf("latest = %v, want %v", got, want)
	}
}

// "latest" without dates: a series added without one goes by its
// newest episode, and what has no date at all comes last.
func TestLatestWithoutDates(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	day := func(d int) time.Time { return time.Date(2026, 10, d, 12, 0, 0, 0, time.UTC) }
	one := 1
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "movie", Name: "Undated film", SortName: "a", LibraryID: "lib"},
		{ID: id(1), Kind: "series", Name: "Undated show", SortName: "b", LibraryID: "lib"},
		{ID: id(2), Kind: "episode", Name: "E1", SortName: "c", SeriesID: id(1), SeasonNumber: &one, EpisodeNumber: &one, LibraryID: "lib", AddedAt: day(5)},
		{ID: id(3), Kind: "series", Name: "Empty undated show", SortName: "d", LibraryID: "lib"},
		{ID: id(4), Kind: "movie", Name: "Film", SortName: "e", LibraryID: "lib", AddedAt: day(10)},
		{ID: id(5), Kind: "series", Name: "Show", SortName: "f", LibraryID: "lib", AddedAt: day(1)},
	}, 1)
	for _, desc := range []bool{true, false} {
		got, err := s.ListCatalog(ctx, ListQuery{Kinds: []string{"movie", "series"}, Sort: "latest", Descending: desc, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{id(4), id(1), id(5), id(0), id(3)}
		if !desc {
			want = []string{id(5), id(1), id(4), id(0), id(3)}
		}
		if !slices.Equal(got, want) {
			t.Errorf("descending %v: %v, want %v", desc, got, want)
		}
	}
}

// Genres (TMDB's) count a library's films and series, and only what the
// profile may see.
func TestCatalogGenres(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "movie", Name: "A", SortName: "a", LibraryID: "films", TMDB: "1", Genres: []string{"Jellyfin's"}},
		{ID: id(1), Kind: "movie", Name: "B", SortName: "b", LibraryID: "films", TMDB: "2", Tags: []string{"hidden"}},
		{ID: id(2), Kind: "series", Name: "C", SortName: "c", LibraryID: "tv", TMDB: "3"},
	}, 1)
	// A page without genres (null in its JSON) counts for none.
	st := Title{Kind: "movie", TMDB: "4", Name: "4"}
	s.PutCatalog(ctx, []CatalogItem{{ID: id(3), Kind: "movie", Name: "D", SortName: "d", LibraryID: "films", TMDB: "4"}}, 1)
	s.SetTitleDetails(ctx, st, []byte(`{"genres":null}`), time.Now())
	for tmdb, genres := range map[string][]string{"1": {"Drama", "Crime"}, "2": {"Drama"}, "3": {"Comedy"}} {
		det, _ := json.Marshal(TitleDetails{Genres: genres})
		kind := "movie"
		if tmdb == "3" {
			kind = "series"
		}
		s.SetTitleDetails(ctx, Title{Kind: kind, TMDB: tmdb, Name: tmdb}, det, time.Now())
	}
	got, err := s.CatalogGenres(ctx, "films", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []GenreCount{{"Crime", 1}, {"Drama", 2}}; !slices.Equal(got, want) {
		t.Errorf("films = %v, want %v", got, want)
	}
	got, _ = s.CatalogGenres(ctx, "films", nil, &Access{BlockedTags: []string{"hidden"}})
	if want := []GenreCount{{"Crime", 1}, {"Drama", 1}}; !slices.Equal(got, want) {
		t.Errorf("with a hidden tag = %v, want %v", got, want)
	}
}

// Listings sort and filter by what TMDB says (names without accents or
// articles, years, genres), not by the index's own names.
func TestListingsUseTMDB(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "movie", Name: "Aaa (Jellyfin)", SortName: "aaa", TMDB: "1", Year: 1990},
		{ID: id(1), Kind: "movie", Name: "Bravo", SortName: "bravo", TMDB: "2"},
		{ID: id(2), Kind: "movie", Name: "home video", SortName: "home video"},
	}, 1)
	for tmdb, page := range map[string]struct {
		name  string
		year  int
		genre string
	}{"1": {"L'Écume", 2004, "Drame"}, "2": {"Bravo", 1999, "Comédie"}} {
		det, _ := json.Marshal(TitleDetails{Genres: []string{page.genre}})
		s.SetTitleDetails(ctx, Title{Kind: "movie", TMDB: tmdb, Name: page.name, Year: page.year}, det, time.Now())
	}
	list := func(q ListQuery) []string {
		t.Helper()
		q.Kinds, q.Limit = []string{"movie"}, 10
		if q.Sort == "" {
			q.Sort = "name"
		}
		got, err := s.ListCatalog(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	// "L'Écume" sorts as "ecume": after "Bravo", before "home video".
	if got := list(ListQuery{}); !slices.Equal(got, []string{id(1), id(0), id(2)}) {
		t.Errorf("by name: %v", got)
	}
	if got := list(ListQuery{YearFrom: 2000}); !slices.Equal(got, []string{id(0)}) {
		t.Errorf("TMDB's year: %v", got)
	}
	if got := list(ListQuery{Genres: []string{"Comédie"}}); !slices.Equal(got, []string{id(1)}) {
		t.Errorf("TMDB's genre: %v", got)
	}
}

// The genres and languages listings go by follow each title's page: those
// kept before migrations 060 and 061, then each page as it's replaced,
// until it's deleted.
func TestDetailsInStep(t *testing.T) {
	s := openAt(t, 59)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "movie", Name: "A", SortName: "a", TMDB: "1"},
		{ID: id(1), Kind: "movie", Name: "B", SortName: "b", TMDB: "2"},
	}, 1)
	old := time.Now().Add(-time.Hour)
	oldTitleDetails(t, s, Title{Kind: "movie", TMDB: "1", Name: "1"}, `{"genres":["Drama","Crime"],"language":"ja"}`, old)
	oldTitleDetails(t, s, Title{Kind: "movie", TMDB: "2", Name: "2"}, `{"genres":null}`, old)
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	genre := func(g string) []string {
		t.Helper()
		got, err := s.ListCatalog(ctx, ListQuery{Kinds: []string{"movie"}, Genres: []string{g}, Sort: "name", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	japanese := func() []string {
		t.Helper()
		got, err := s.ListCatalog(ctx, ListQuery{Sort: "name", Limit: 10,
			Category: &CategoryFilter{Rules: CategoryRules{Languages: []string{"ja"}}}})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := genre("Crime"); !slices.Equal(got, []string{id(0)}) {
		t.Errorf("kept before: %v", got)
	}
	if got := japanese(); !slices.Equal(got, []string{id(0)}) {
		t.Errorf("language kept before: %v", got)
	}
	s.SetTitleDetails(ctx, Title{Kind: "movie", TMDB: "1", Name: "1"}, []byte(`{"genres":["Comedy"]}`), time.Now())
	s.SetTitleDetails(ctx, Title{Kind: "movie", TMDB: "2", Name: "2"}, []byte(`{"genres":["Crime"],"language":"ja"}`), time.Now())
	if got := genre("Crime"); !slices.Equal(got, []string{id(1)}) {
		t.Errorf("replaced: %v", got)
	}
	if got := japanese(); !slices.Equal(got, []string{id(1)}) {
		t.Errorf("language replaced: %v", got)
	}
	if got, _ := s.LibraryLanguages(ctx); !slices.Equal(got, []string{"ja"}) {
		t.Errorf("languages: %v", got)
	}
	if got, _ := s.CatalogGenres(ctx, "", nil, nil); !slices.Equal(got, []GenreCount{{"Comedy", 1}, {"Crime", 1}}) {
		t.Errorf("genres: %v", got)
	}
	if err := s.ForgetTMDB(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM catalog_details_genres) + (SELECT count(*) FROM catalog_details_language)`).Scan(&n)
	if n != 0 {
		t.Errorf("%d genres and languages left after the pages went", n)
	}
}
