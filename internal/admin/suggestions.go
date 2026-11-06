package admin

import (
	"sort"
	"time"

	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/valuation"
)

// suggestionOrder orders what the libraries lack for list=suggestions: the
// picks by value, the most valuable first, each title once: a series is
// one title to add, its seasons together, in order, at its most valuable
// season's place.
func suggestionOrder(rows []valuation.Value) []valuation.Value {
	picks := append([]valuation.Value(nil), rows...)
	sort.SliceStable(picks, func(i, j int) bool { return picks[i].Score > picks[j].Score })
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
	for _, next := range picks {
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
	}
	return out
}

// outSoon keeps the titles out at home, or out within store.ReleaseSoon:
// nothing to add before. Seasons come only once aired or about to
// (store's picks); a film by its release (store.HomeRelease.OutBy).
func outSoon(rows []valuation.Value, releases map[string]store.HomeRelease, now time.Time) []valuation.Value {
	by := now.Add(store.ReleaseSoon)
	kept := rows[:0:0]
	for _, v := range rows {
		if r, _ := store.ParseTitleKey(v.Key); r.Kind == "movie" && !releases[r.TMDB()].OutBy(by) {
			continue
		}
		kept = append(kept, v)
	}
	return kept
}
