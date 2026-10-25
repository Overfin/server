package admin

import (
	"sort"

	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/valuation"
)

// suggestionOrder orders what the libraries lack for list=suggestions:
// of every hundred titles, share come from TMDB's trending and popular
// lists (by place, trending first at the same place), whatever their
// value; the rest are the picks, by value. Either fills in when the other
// runs out, and a title comes once. lists are the trending and popular
// places by kind and TMDB ID ("movie/550").
func suggestionOrder(rows []valuation.Value, lists []map[string]int, share int) []valuation.Value {
	type ranked struct {
		v        valuation.Value
		position int
		list     int
		season   int
	}
	var discovered []ranked
	for _, v := range rows {
		r, _ := store.ParseTitleKey(v.Key)
		top := "movie/"
		if r.Kind != "movie" {
			top = "series/"
		}
		best := ranked{v: v, position: -1, season: r.Season}
		for i, positions := range lists {
			if n, ok := positions[top+r.TMDB()]; ok && (best.position < 0 || n < best.position) {
				best.position, best.list = n, i
			}
		}
		if best.position >= 0 {
			discovered = append(discovered, best)
		}
	}
	sort.SliceStable(discovered, func(i, j int) bool {
		a, b := discovered[i], discovered[j]
		if a.position != b.position {
			return a.position < b.position
		}
		if a.list != b.list {
			return a.list < b.list
		}
		return a.season < b.season
	})
	picks := append([]valuation.Value(nil), rows...)
	sort.SliceStable(picks, func(i, j int) bool { return picks[i].Score > picks[j].Score })

	out := make([]valuation.Value, 0, len(rows))
	given := map[string]bool{}
	taken := 0
	for len(out) < len(rows) {
		fromDiscovery := len(discovered) > 0 && (len(picks) == 0 || taken*100 < share*(len(out)+1))
		var next valuation.Value
		if fromDiscovery {
			next, discovered = discovered[0].v, discovered[1:]
			taken++
		} else if len(picks) > 0 {
			next, picks = picks[0], picks[1:]
		} else {
			break
		}
		if !given[next.Key] {
			given[next.Key] = true
			out = append(out, next)
		}
	}
	return out
}
