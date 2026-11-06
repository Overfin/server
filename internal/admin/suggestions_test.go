package admin

import (
	"strings"
	"testing"

	"github.com/overfin/server/internal/valuation"
)

// Suggestions are the picks by value, each title once: a series' seasons
// come together, in order, at its most valuable season's place.
func TestSuggestionOrder(t *testing.T) {
	rows := []valuation.Value{
		{Title: &valuation.Title{Key: "movie/tmdb/1"}, Score: 90},
		{Title: &valuation.Title{Key: "series/tmdb/7/s2"}, Score: 85},
		{Title: &valuation.Title{Key: "movie/tmdb/2"}, Score: 80},
		{Title: &valuation.Title{Key: "series/tmdb/7/s1"}, Score: 3},
		{Title: &valuation.Title{Key: "movie/tmdb/3"}, Score: 70},
	}
	var out []string
	for _, v := range suggestionOrder(rows) {
		out = append(out, strings.TrimPrefix(strings.TrimPrefix(v.Key, "movie/tmdb/"), "series/tmdb/"))
	}
	if got := strings.Join(out, " "); got != "1 7/s1 7/s2 2 3" {
		t.Errorf("order: %s", got)
	}
}
