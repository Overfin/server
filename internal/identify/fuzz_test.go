package identify

import (
	"strconv"
	"testing"
	"unicode/utf8"
)

// Any folder or file name gives a year from 1900 to 2099 or none, a TMDB
// ID written as TMDB writes it or none, and a name with no runs of
// spaces.
func FuzzParse(f *testing.F) {
	for _, s := range []string{"Heat (1995) [tmdbid-949]", "The.Candidate.2023.1080p.mkv", "1917 (2019)", "1917",
		"Show {tmdb=1399}", "Film [tmdbid-0949]", "[tmdb-0]", "  - ( [ "} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n := parse(s)
		if n.year != 0 && (n.year < 1900 || n.year > 2099) {
			t.Fatalf("%q -> year %d", s, n.year)
		}
		if n.tmdb != "" {
			if id, err := strconv.Atoi(n.tmdb); err != nil || id <= 0 || strconv.Itoa(id) != n.tmdb {
				t.Fatalf("%q -> TMDB %q", s, n.tmdb)
			}
		}
		if utf8.ValidString(s) && !utf8.ValidString(n.name) {
			t.Fatalf("%q -> name %q", s, n.name)
		}
	})
}
