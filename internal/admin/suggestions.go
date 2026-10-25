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
// runs out, and a title comes once (a series with all its seasons). lists
// are the trending and popular
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

	// A series is one title to add: its seasons come together, in order, at
	// its first season's place, and take one place in the mix.
	seasons := map[string][]valuation.Value{}
	for _, v := range rows {
		if r, _ := store.ParseTitleKey(v.Key); r.Kind == "season" {
			seasons[r.Series().Key()] = append(seasons[r.Series().Key()], v)
		}
	}
	for _, list := range seasons {
		sort.SliceStable(list, func(i, j int) bool {
			a, _ := store.ParseTitleKey(list[i].Key)
			b, _ := store.ParseTitleKey(list[j].Key)
			return a.Season < b.Season
		})
	}
	out := make([]valuation.Value, 0, len(rows))
	given := map[string]bool{}
	titles, taken := 0, 0
	for len(discovered) > 0 || len(picks) > 0 {
		var next valuation.Value
		fromDiscovery := len(discovered) > 0 && (len(picks) == 0 || taken*100 < share*(titles+1))
		if fromDiscovery {
			next, discovered = discovered[0].v, discovered[1:]
		} else {
			next, picks = picks[0], picks[1:]
		}
		title := next.Key
		group := []valuation.Value{next}
		if r, _ := store.ParseTitleKey(next.Key); r.Kind == "season" {
			title = r.Series().Key()
			group = seasons[title]
		}
		if given[title] {
			continue
		}
		given[title] = true
		out = append(out, group...)
		titles++
		if fromDiscovery {
			taken++
		}
	}
	return out
}
