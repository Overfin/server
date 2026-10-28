package store

import "testing"

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
