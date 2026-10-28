package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// A title's age: the ratings country's, else the middle of the others'.
func TestTitleAge(t *testing.T) {
	for _, tc := range []struct {
		kind    string
		ratings map[string][]string
		country string
		want    int
		ok      bool
	}{
		{"movie", map[string][]string{"US": {"PG-13"}, "FR": {"TP"}}, "US", 13, true},
		{"movie", map[string][]string{"US": {"PG-13"}, "FR": {"TP"}}, "FR", 0, true},
		// No Canadian rating: the middle of 0, 12 and 16.
		{"movie", map[string][]string{"FR": {"TP"}, "DE": {"12"}, "GB": {"15", "18"}}, "CA", 12, true},
		// Even: the higher middle one (of 6, 12, 16, 18).
		{"movie", map[string][]string{"DE": {"6"}, "FR": {"12"}, "NL": {"16"}, "GB": {"18"}}, "CA", 16, true},
		{"series", map[string][]string{"US": {"TV-Y7"}}, "CA", 7, true},
		{"series", map[string][]string{"CA": {"C8"}, "US": {"TV-MA"}}, "CA", 8, true},
		// Ratings as TMDB writes them.
		{"movie", map[string][]string{"AU": {"MA 15+"}}, "AU", 15, true},
		{"movie", map[string][]string{"FR": {"TP"}}, "FR", 0, true},
		{"movie", map[string][]string{"KR": {"19"}}, "KR", 19, true},
		{"series", map[string][]string{"SE": {"Från 11 år"}}, "SE", 11, true},
		{"series", map[string][]string{"IE": {"15"}}, "IE", 15, true},
		// What says nothing of age, or a country not in the table.
		{"movie", map[string][]string{"US": {"NR"}, "XX": {"9"}}, "US", 0, false},
		{"movie", nil, "US", 0, false},
	} {
		if got, ok := TitleAge(tc.kind, tc.ratings, tc.country); got != tc.want || ok != tc.ok {
			t.Errorf("%v in %s: %d %v, want %d %v", tc.ratings, tc.country, got, ok, tc.want, tc.ok)
		}
	}
	// Every age in the table is one a person can be.
	for _, table := range []map[string]map[string]int{filmAges, tvAges} {
		for c, ratings := range table {
			for r, age := range ratings {
				if age < 0 || age > 21 {
					t.Errorf("%s %s: %d", c, r, age)
				}
			}
		}
	}
}

// Titles' ages are kept with their ratings, worked out again when the
// ratings country changes, and dropped with their ratings.
func TestTitleAgesKept(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	age := func(kind, tmdb string) (int, bool) {
		t.Helper()
		var a int
		err := s.db.QueryRowContext(ctx, `SELECT age FROM title_ages WHERE kind = ? AND tmdb = ?`, kind, tmdb).Scan(&a)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false
		}
		if err != nil {
			t.Fatal(err)
		}
		return a, true
	}
	s.SetTitleRatings(ctx, RatedTitle{"movie", "1"}, map[string][]string{"US": {"R"}, "FR": {"12"}}, "", "", now)
	s.SetTitleRatings(ctx, RatedTitle{"series", "1"}, map[string][]string{"US": {"TV-Y"}}, "", "", now)
	s.SetTitleRatings(ctx, RatedTitle{"movie", "2"}, map[string][]string{"US": {NotRated}}, "", "", now)
	if a, ok := age("movie", "1"); !ok || a != 17 {
		t.Errorf("film in the US: %d %v", a, ok)
	}
	if a, ok := age("series", "1"); !ok || a != 0 {
		t.Errorf("series: %d %v", a, ok)
	}
	if _, ok := age("movie", "2"); ok {
		t.Error("an unrated film has an age")
	}
	// The ratings country's own first.
	if err := s.SetRatingsCountry(ctx, "FR"); err != nil {
		t.Fatal(err)
	}
	if a, ok := age("movie", "1"); !ok || a != 12 {
		t.Errorf("film in France: %d %v", a, ok)
	}
	// Ratings fetched again without any: no age.
	s.SetTitleRatings(ctx, RatedTitle{"movie", "1"}, nil, "", "", now)
	if _, ok := age("movie", "1"); ok {
		t.Error("an age kept without ratings")
	}
	// Purged with the ratings.
	if err := s.PurgeRatings(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, ok := age("series", "1"); ok {
		t.Error("an age kept after its ratings were purged")
	}
}

// Kids profiles' ceilings from before ages become ages once: the younger
// of the two, in the ratings country.
func TestAgesFromCeilings(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	acct, _, err := s.CreateAccount(ctx, "subject-1", owner("1"))
	if err != nil {
		t.Fatal(err)
	}
	add := func(name, movie, series string) string {
		t.Helper()
		p, err := s.AddProfile(ctx, acct.ID, NewProfile{Name: name, Kind: "kids"}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE profiles SET movie_ceiling = ?, series_ceiling = ? WHERE id = ?`,
			nullable(movie), nullable(series), p.ID); err != nil {
			t.Fatal(err)
		}
		return p.ID
	}
	both := add("Both", "PG-13", "TV-Y7")
	one := add("One", "PG", "")
	unknown := add("Unknown", "PG", "XYZ")
	n, err := s.AgesFromCeilings(ctx)
	if err != nil || n != 1 {
		t.Fatalf("converted %d %v", n, err)
	}
	seven := 7
	for _, tc := range []struct {
		id   string
		want *int
	}{{both, &seven}, {one, nil}, {unknown, nil}} {
		p, err := s.Profile(ctx, acct.ID, tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if (p.MaxAge == nil) != (tc.want == nil) || (p.MaxAge != nil && *p.MaxAge != *tc.want) {
			t.Errorf("%s: %v, want %v", p.Name, p.MaxAge, tc.want)
		}
	}
	// Once only.
	if n, err := s.AgesFromCeilings(ctx); err != nil || n != 0 {
		t.Errorf("again: %d %v", n, err)
	}
}
