package api

import (
	"context"
	"hash/fnv"
	"net/http"
	"slices"
	"time"

	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
)

// Recommendations: the library's films and series the profile hasn't
// watched, ordered by how well they fit it. Worked out for the page being
// shown, from the profile's own viewing and its account's tastes, and kept
// nowhere (rule 40).

// recentPool is how many of the newest titles "recent" recommendations
// choose among.
const recentPool = 200

// kidsGenres are TMDB's genres a kids title has one of (with an age of
// kidsAge at most): Family, Kids, Animation.
var kidsGenres = []int{10751, 10762, 16}

const kidsAge = 8

func (l Library) recommended(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r, "scope", "limit") {
		return
	}
	q := r.URL.Query()
	scope := q.Get("scope")
	limit, ok := parseLimit(cmpOr(q.Get("limit"), "20"))
	if !ok || (scope != "" && scope != "all" && scope != "recent") {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	ctx := r.Context()
	out := []library.TitleSummary{}
	if !l.indexReady(r) {
		writeOK(w, map[string]any{"items": out})
		return
	}
	keys, err := l.recommend(ctx, p, scope == "recent", min(limit, 50))
	if err != nil {
		l.storeError(w, err)
		return
	}
	if len(keys) > 0 {
		if out, err = l.titleSummaries(ctx, p, keys); err != nil {
			l.storeError(w, err)
			return
		}
	}
	writeOK(w, map[string]any{"items": out})
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// recommend returns the title keys that fit the profile best, best first:
// among every film and series it may see and hasn't watched or started,
// or among the newest only.
func (l Library) recommend(ctx context.Context, p *store.Profile, recent bool, n int) ([]string, error) {
	st := l.Provisioner.Store
	a, err := l.access(ctx, p)
	if err != nil {
		return nil, err
	}
	// Every film and series it may see (the cheapest listing: the SQL
	// for "unwatched" and "latest" is slow on a large library); what it
	// watched or started comes from its own history.
	ids, err := st.ListCatalog(ctx, store.ListQuery{Kinds: []string{"movie", "series"}, Access: a, Sort: "addedAt", Limit: -1})
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	facts, err := st.FactsOfItems(ctx, ids)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	watched, seen, err := st.WatchedGenres(ctx, p.ID, now)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(facts))
	for k := range facts {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	if recent {
		slices.SortFunc(keys, func(x, y string) int { return facts[y].Latest.Compare(facts[x].Latest) })
		keys = keys[:min(len(keys), recentPool)]
	}
	tastes, err := st.AccountTastes(ctx, p.AccountID)
	if err != nil {
		return nil, err
	}
	kids, err := l.wantsKids(ctx, p, tastes)
	if err != nil {
		return nil, err
	}
	fit := fitter{watched: watched, tastes: tastes, kids: kids, kidsProfile: p.Kind == "kids", day: now.Format(time.DateOnly)}
	type scored struct {
		key   string
		score float64
	}
	var list []scored
	for _, k := range keys {
		if score, ok := fit.score(facts[k]); ok {
			list = append(list, scored{k, score})
		}
	}
	slices.SortFunc(list, func(x, y scored) int {
		switch {
		case x.score > y.score:
			return -1
		case x.score < y.score:
			return 1
		}
		return compareStrings(x.key, y.key)
	})
	out := make([]string, 0, n)
	for _, s := range list[:min(len(list), n)] {
		out = append(out, s.key)
	}
	return out, nil
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// wantsKids says whether a profile's recommendations include children's
// content: a kids profile's do; another's when it set "Contenu pour
// enfants".
func (l Library) wantsKids(_ context.Context, p *store.Profile, _ *store.Tastes) (bool, error) {
	return p.Kind == "kids" || (p.KidsTitles != nil && *p.KidsTitles), nil
}

// fitter scores how well a title fits a profile.
type fitter struct {
	watched     map[int]float64 // genres the profile watched, weighed
	tastes      *store.Tastes   // what its account said; nil if never asked
	kids        bool
	kidsProfile bool
	day         string // the day's own small shuffle among equals
}

// score is a title's fit, about 0 to 1.5; ok is false for a title left out
// (a kids title, for a profile that wants none).
func (f fitter) score(t *store.TitleFacts) (float64, bool) {
	if t == nil {
		return 0, false
	}
	kidsTitle := t.Age != nil && *t.Age <= kidsAge && slices.ContainsFunc(t.Genres, func(g int) bool { return slices.Contains(kidsGenres, g) })
	if kidsTitle && !f.kids && !f.kidsProfile {
		return 0, false
	}
	score := 0.0
	// What it watches: its genres, each against the genre it watches most.
	top := 0.0
	for _, w := range f.watched {
		top = max(top, w)
	}
	if top > 0 && len(t.Genres) > 0 {
		sum := 0.0
		for _, g := range t.Genres {
			sum += f.watched[g] / top
		}
		score += 0.6 * sum / float64(len(t.Genres))
	}
	// What its account said.
	if f.tastes != nil {
		if slices.ContainsFunc(t.Genres, func(g int) bool { return slices.Contains(f.tastes.Genres, g) }) {
			score += 0.25
		}
		if (f.tastes.Prefer == "series" && t.Kind == "series") || (f.tastes.Prefer == "movies" && t.Kind == "movie") {
			score += 0.15
		}
	}
	if kidsTitle && f.kids {
		score += 0.3
	}
	// Good titles first among equals, and a little change from day to day.
	score += 0.2 * min(max(t.Rating, 0), 10) / 10
	h := fnv.New32a()
	h.Write([]byte(f.day + t.Key))
	score += 0.05 * float64(h.Sum32()%1000) / 1000
	return score, true
}
