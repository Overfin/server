package admin

import (
	"testing"

	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/valuation"
)

// Films are numbered among films and series among series, in the order of
// their values; seasons and what the libraries lack get no rank.
func TestRanksOf(t *testing.T) {
	value := func(key string, in bool) valuation.Value {
		return valuation.Value{Title: &valuation.Title{Key: key, InLibrary: in}}
	}
	got := ranksOf([]valuation.Value{
		value("series/tmdb/1", true), value("series/tmdb/1/s1", true), value("movie/tmdb/2", true),
		value("movie/tmdb/3", false), value("series/tmdb/4", true), value("movie/tmdb/5", true),
	})
	want := map[string]store.TitleRank{"series/tmdb/1": {Kind: "series", Rank: 1}, "movie/tmdb/2": {Kind: "movie", Rank: 1},
		"series/tmdb/4": {Kind: "series", Rank: 2}, "movie/tmdb/5": {Kind: "movie", Rank: 2}}
	if len(got) != len(want) {
		t.Fatalf("ranks: %v", got)
	}
	for k, r := range want {
		if got[k] != r {
			t.Errorf("%s: %v, want %v", k, got[k], r)
		}
	}
}
