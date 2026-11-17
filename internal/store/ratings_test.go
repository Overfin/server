package store

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRatings(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.SetRatingsCountry(ctx, "US"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	us := []RatingLevel{{"NR", "", 0}, {"G", "", 1}, {"PG", "", 2}, {"PG-13", "", 3}, {"R", "", 4}}
	tv := []RatingLevel{{"TV-Y", "", 1}, {"TV-PG", "", 3}, {"TV-14", "", 4}}
	if err := s.SetRatingScales(ctx, "movie", map[string][]RatingLevel{"US": us}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRatingScales(ctx, "series", map[string][]RatingLevel{"US": tv}); err != nil {
		t.Fatal(err)
	}
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	one := 1
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "movie", Name: "Pixar", SortName: "a", TMDB: "1"},   // G
		{ID: id(1), Kind: "movie", Name: "Heat", SortName: "b", TMDB: "2"},    // R
		{ID: id(2), Kind: "movie", Name: "Cut", SortName: "c", TMDB: "3"},     // PG and an R cut
		{ID: id(3), Kind: "movie", Name: "Nothing", SortName: "d", TMDB: "4"}, // fetched, no US rating
		{ID: id(4), Kind: "movie", Name: "Unrated", SortName: "e", TMDB: "5"}, // NR
		{ID: id(5), Kind: "movie", Name: "No TMDB", SortName: "f"},            // nothing to go by
		{ID: id(6), Kind: "movie", Name: "Not yet", SortName: "g", TMDB: "6"}, // not fetched
		{ID: id(7), Kind: "series", Name: "Bluey", SortName: "h", TMDB: "10"}, // TV-Y
		{ID: id(8), Kind: "episode", Name: "Ep", SortName: "i", SeriesID: id(7), SeasonNumber: &one, EpisodeNumber: &one},
		{ID: id(9), Kind: "movie", Name: "Odd", SortName: "j", TMDB: "7"}, // a rating not on the scale
	}, 1)
	for tmdb, r := range map[string]map[string][]string{
		"1": {"US": {"G"}, "FR": {"18"}}, "2": {"US": {"R"}}, "3": {"US": {"PG", "R"}},
		"4": {"FR": {"TP"}}, "5": {"US": {"NR"}}, "7": {"US": {"X"}},
	} {
		if err := s.SetTitleRatings(ctx, RatedTitle{"movie", tmdb}, r, "", "", now); err != nil {
			t.Fatal(err)
		}
	}
	s.SetTitleRatings(ctx, RatedTitle{"series", "10"}, map[string][]string{"US": {"TV-Y"}}, "", "", now)

	allowed := func(c Ceiling) []string {
		got, err := s.ListCatalog(ctx, ListQuery{Sort: "name", Access: &Access{Ceiling: &c}, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	// By age: Pixar (G, 0), Nothing (France's U, 0: no US rating, so
	// France's), Bluey and its episode (TV-Y, 0) for 8; Heat (R, 17) and Cut
	// (PG and R: the higher, 17) too for 17. Unrated, Odd (not on any
	// scale), No TMDB and Not yet have no age: hidden.
	if got := allowed(Ceiling{8}); !slices.Equal(got, []string{id(0), id(3), id(7), id(8)}) {
		t.Errorf("8: %v", got)
	}
	if got := allowed(Ceiling{17}); !slices.Equal(got, []string{id(0), id(1), id(2), id(3), id(7), id(8)}) {
		t.Errorf("17: %v", got)
	}
	if got := allowed(Ceiling{-1}); len(got) != 0 {
		t.Errorf("no age chosen: %v", got)
	}
	if n, err := s.CountCatalog(ctx, ListQuery{Access: &Access{Ceiling: &Ceiling{8}}}); err != nil || n != 4 {
		t.Errorf("count: %d %v", n, err)
	}
	got, indexed, err := s.AllowedItems(ctx, Access{Ceiling: &Ceiling{8}}, []string{id(1), id(8), "ffffffffffffffffffffffffffffffff"})
	if err != nil || len(got) != 1 || !got[id(8)] || len(indexed) != 2 {
		t.Errorf("items: %v %v %v", got, indexed, err)
	}
	if ok, err := s.TitleAllowed(ctx, Ceiling{8}, "movie", "1"); err != nil || !ok {
		t.Errorf("title 1: %v %v", ok, err)
	}
	if ok, _ := s.TitleAllowed(ctx, Ceiling{8}, "movie", "2"); ok {
		t.Error("title 2 allowed")
	}
	// The ratings country's own rating first: in France, Pixar is 18.
	if err := s.SetRatingsCountry(ctx, "FR"); err != nil {
		t.Fatal(err)
	}
	if got := allowed(Ceiling{8}); !slices.Equal(got, []string{id(3), id(7), id(8)}) {
		t.Errorf("8, in France: %v", got)
	}
	s.SetRatingsCountry(ctx, "US")

	// Titles still to fetch: the unfetched one, then none once fetched.
	todo, err := s.TitlesToRate(ctx, now.Add(-time.Hour), now.Add(-time.Hour), "2026-10-22", 50)
	if err != nil || len(todo) != 1 || todo[0] != (RatedTitle{"movie", "6"}) {
		t.Errorf("to rate: %v %v", todo, err)
	}
	// Titles not at home yet, out in the last year or coming, are fetched
	// again sooner; one at home isn't, nor one with no date at all or out
	// long ago (asked weekly, they'd be asked forever).
	s.SetTitleRatings(ctx, RatedTitle{"movie", "1"}, map[string][]string{"US": {"G"}}, "2020-01-01", "2020-06-01", now)
	s.SetTitleRatings(ctx, RatedTitle{"movie", "2"}, map[string][]string{"US": {"R"}}, "2026-09-01", "", now)
	s.SetTitleRatings(ctx, RatedTitle{"movie", "3"}, map[string][]string{"US": {"PG"}}, "1999-01-01", "", now)
	todo, _ = s.TitlesToRate(ctx, now.Add(-time.Hour), now.Add(time.Hour), "2026-10-22", 50)
	var keys []string
	for _, t := range todo {
		keys = append(keys, t.TMDB)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"2", "6"}) {
		t.Errorf("pending ones again: %v", todo)
	}
	if r, h, ok, _ := s.TitleRelease(ctx, "movie", "1"); !ok || r != "2020-01-01" || h != "2020-06-01" {
		t.Errorf("release: %q %q %v", r, h, ok)
	}
	if f, total, err := s.RatingProgress(ctx); err != nil || f != 7 || total != 8 {
		t.Errorf("progress: %d/%d %v", f, total, err)
	}
	// Kept five months at most, then hidden again.
	if err := s.PurgeRatings(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := allowed(Ceiling{99}); len(got) != 0 {
		t.Errorf("after the purge: %v", got)
	}
}

// With no country chosen, the ratings country is the server's region:
// the first of its languages with a region Overfin reads ratings in;
// none otherwise, never the US. A choice wins over it, and titles' ages
// follow whichever applies.
func TestRatingsCountryFromRegion(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	country := func() string {
		t.Helper()
		c, err := s.RatingsCountry(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	age := func() int {
		t.Helper()
		var a int
		if err := s.db.QueryRowContext(ctx, `SELECT age FROM title_ages WHERE kind = 'movie' AND tmdb = '1'`).Scan(&a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	// 0 in France, 13 in the US, 14 in Canada, 18 in Germany: the middle
	// is 14 (the higher of 13 and 14).
	s.SetTitleRatings(ctx, RatedTitle{"movie", "1"}, map[string][]string{"FR": {"TP"}, "US": {"PG-13"}, "CA": {"14A"}, "DE": {"18"}}, "", "", time.Now())

	// Languages not set yet: none.
	if c := country(); c != "" {
		t.Errorf("fresh: %q", c)
	}
	for _, tc := range []struct {
		offered []string
		def     string
		want    string
	}{
		{[]string{"fr-CA"}, "fr-CA", "CA"},
		{[]string{"en-US", "en-GB"}, "en-GB", "GB"},
		{[]string{"pt-BR"}, "pt-BR", "BR"},
		// No region.
		{[]string{"fr"}, "fr", ""},
		// A region Overfin has no ages for.
		{[]string{"pl-PL"}, "pl-PL", ""},
		// The first with one, the default first.
		{[]string{"pl-PL", "de-DE"}, "pl-PL", "DE"},
	} {
		if _, err := s.SetLanguages(ctx, tc.offered, tc.def); err != nil {
			t.Fatal(err)
		}
		if c := country(); c != tc.want {
			t.Errorf("%v: %q, want %q", tc.offered, c, tc.want)
		}
	}
	// Ages follow the region as the languages change.
	if a := age(); a != 18 {
		t.Errorf("in Germany: %d", a)
	}
	if _, err := s.SetLanguages(ctx, []string{"fr"}, "fr"); err != nil {
		t.Fatal(err)
	}
	if a := age(); a != 14 {
		t.Errorf("no country: %d", a)
	}
	if _, err := s.SetLanguages(ctx, []string{"fr-FR"}, "fr-FR"); err != nil {
		t.Fatal(err)
	}
	if a := age(); a != 0 {
		t.Errorf("in France: %d", a)
	}

	// A choice wins, whatever the languages.
	if err := s.SetRatingsCountry(ctx, "US"); err != nil {
		t.Fatal(err)
	}
	if c := country(); c != "US" || age() != 13 {
		t.Errorf("chosen: %q, age %d", c, age())
	}
	if _, err := s.SetLanguages(ctx, []string{"en-CA"}, "en-CA"); err != nil {
		t.Fatal(err)
	}
	if c, ok, _ := s.ChosenRatingsCountry(ctx); c != "US" || !ok || country() != "US" || age() != 13 {
		t.Errorf("chosen, languages changed: %q %v %q", c, ok, country())
	}
	// "" follows the region again.
	if err := s.SetRatingsCountry(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.ChosenRatingsCountry(ctx); ok || country() != "CA" || age() != 14 {
		t.Errorf("following again: %v %q, age %d", ok, country(), age())
	}
}
