package api

import (
	"slices"
	"testing"
)

// Titles found by another database's ID, kept of the kind asked, in their
// order: any kind, all; another, those of the kind and the library's items
// TMDB doesn't know (they may be one), but for series, series only; a key
// that isn't one, never.
func TestOfKind(t *testing.T) {
	keys := []string{"movie/tmdb/1", "series/tmdb/2", "item/" + "0123456789abcdef0123456789abcdef", "series/tmdb/2/s1", "not a key"}
	for kind, want := range map[string][]string{
		"":       keys,
		"movie":  {"movie/tmdb/1", "item/0123456789abcdef0123456789abcdef"},
		"series": {"series/tmdb/2"},
		"season": {"item/0123456789abcdef0123456789abcdef", "series/tmdb/2/s1"},
	} {
		if got := (Library{}).ofKind(keys, kind); !slices.Equal(got, want) {
			t.Errorf("%q: %q, want %q", kind, got, want)
		}
	}
}
