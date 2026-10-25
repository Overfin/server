package admin

import (
	"strings"
	"testing"

	"github.com/overfin/server/internal/valuation"
)

// Suggestions mix the picks, by value, and trending and popular titles,
// by place, by the share; a title comes once, and either side fills in
// when the other runs out.
func TestSuggestionOrder(t *testing.T) {
	rows := []valuation.Value{
		{Title: &valuation.Title{Key: "movie/tmdb/1"}, Score: 90},
		{Title: &valuation.Title{Key: "movie/tmdb/2"}, Score: 80},
		{Title: &valuation.Title{Key: "movie/tmdb/3"}, Score: 70},
		{Title: &valuation.Title{Key: "movie/tmdb/4"}, Score: 10},
		{Title: &valuation.Title{Key: "movie/tmdb/5"}, Score: 5},
	}
	trending := map[string]int{"movie/5": 0, "movie/1": 1}
	popular := map[string]int{"movie/4": 0}
	keys := func(vs []valuation.Value) string {
		var out []string
		for _, v := range vs {
			out = append(out, strings.TrimPrefix(v.Key, "movie/tmdb/"))
		}
		return strings.Join(out, " ")
	}
	for _, c := range []struct {
		share int
		want  string
	}{
		{0, "1 2 3 4 5"},
		{100, "5 4 1 2 3"},
		{50, "5 1 4 2 3"},
	} {
		if got := keys(suggestionOrder(rows, []map[string]int{trending, popular}, c.share)); got != c.want {
			t.Errorf("share %d: %s, want %s", c.share, got, c.want)
		}
	}
}
