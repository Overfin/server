package admin

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/valuation"
)

// titleGroups are the field groups /apps/v1/titles gives, each the scope
// of the same name.
var titleGroups = []string{"score", "stats", "availability"}

type appTitleJSON struct {
	// Kind is movie or season; a season carries its series' IDs.
	Kind   string `json:"kind"`
	TMDB   string `json:"tmdbId,omitempty"`
	IMDb   string `json:"imdbId,omitempty"`
	TVDB   string `json:"tvdbId,omitempty"`
	Season *int   `json:"season,omitempty"`
	// Score is the title's value, 0 to 100 (score).
	Score *float64 `json:"score,omitempty"`
	// Stats are what the profiles did with it, in total (stats).
	Stats *titleStatsJSON `json:"stats,omitempty"`
	// InLibrary says whether a library has it (availability).
	InLibrary *bool `json:"inLibrary,omitempty"`
}

// titleStatsJSON: counts below 2 are given as 0, so none points at one
// person.
type titleStatsJSON struct {
	Plays      int    `json:"plays"`
	Viewers    int    `json:"viewers"`
	Favorites  int    `json:"favorites"`
	LastPlayed string `json:"lastPlayed,omitempty"` // YYYY-MM-DD
}

const (
	appTitlesLimit    = 100
	appTitlesMaxLimit = 1000
)

// appTitles (GET /apps/v1/titles) lists the films and seasons Overfin
// values, one row each with every ID known, and the field groups asked
// for (fields=score,stats,availability; all the key may read when left
// out). Filters: inLibrary, kind (movie or season). Sorting: by score
// (needs the score scope) or name, order asc or desc. Pages of limit rows
// (100, at most 1000), the next one at the cursor given.
func (p *Panel) appTitles(w http.ResponseWriter, r *http.Request) {
	k := p.appKey(w, r, "")
	if k == nil {
		return
	}
	q := r.URL.Query()
	for name := range q {
		if !strings.Contains(" fields inLibrary kind sort order limit cursor ", " "+name+" ") {
			invalid(w, "unknown parameter: "+name)
			return
		}
	}
	want := map[string]bool{}
	if f := q.Get("fields"); f != "" {
		for _, g := range strings.Split(f, ",") {
			if !strings.Contains(" "+strings.Join(titleGroups, " ")+" ", " "+g+" ") {
				invalid(w, "fields: score, stats or availability")
				return
			}
			if !allows(k, g) {
				writeJSON(w, http.StatusForbidden, map[string]string{"type": "not-allowed", "detail": "this key may not read " + g})
				return
			}
			want[g] = true
		}
	} else {
		for _, g := range titleGroups {
			if allows(k, g) {
				want[g] = true
			}
		}
	}
	if len(want) == 0 {
		writeJSON(w, http.StatusForbidden, map[string]string{"type": "not-allowed", "detail": "this key may not read titles"})
		return
	}
	sortBy, order := q.Get("sort"), q.Get("order")
	if sortBy == "" {
		sortBy = "name"
		if allows(k, "score") {
			sortBy = "score"
		}
	}
	switch {
	case sortBy != "score" && sortBy != "name":
		invalid(w, "sort: score or name")
		return
	case sortBy == "score" && !allows(k, "score"):
		writeJSON(w, http.StatusForbidden, map[string]string{"type": "not-allowed", "detail": "this key may not sort by score"})
		return
	case order != "" && order != "asc" && order != "desc":
		invalid(w, "order: asc or desc")
		return
	}
	if order == "" {
		order = map[string]string{"score": "desc", "name": "asc"}[sortBy]
	}
	limit, start := appTitlesLimit, 0
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > appTitlesMaxLimit {
			invalid(w, "limit: 1 to "+strconv.Itoa(appTitlesMaxLimit))
			return
		}
		limit = n
	}
	if s := q.Get("cursor"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			invalid(w, "cursor: as given in next")
			return
		}
		start = n
	}
	var inLibrary *bool
	if s := q.Get("inLibrary"); s != "" {
		b, err := strconv.ParseBool(s)
		if err != nil {
			invalid(w, "inLibrary: true or false")
			return
		}
		inLibrary = &b
	}
	kind := q.Get("kind")
	if kind != "" && kind != "movie" && kind != "season" {
		invalid(w, "kind: movie or season")
		return
	}

	ctx := r.Context()
	m, f, err := p.measuredFacts(ctx)
	if err != nil {
		p.fail(w, "measuring the titles", err)
		return
	}
	saved, err := p.Store.ValuationWeights(ctx)
	if err != nil {
		p.fail(w, "reading the valuation's weights", err)
		return
	}
	var rows []valuation.Value
	for _, v := range m.Scored(valuation.Weights(saved)) {
		r, _ := store.ParseTitleKey(v.Key)
		if (inLibrary != nil && v.InLibrary != *inLibrary) || (kind != "" && r.Kind != kind) {
			continue
		}
		rows = append(rows, v)
	}
	less := func(i, j int) bool {
		if sortBy == "score" && rows[i].Score != rows[j].Score {
			return rows[i].Score < rows[j].Score
		}
		if a, b := store.Fold(rows[i].Name), store.Fold(rows[j].Name); a != b {
			return a < b
		}
		return rows[i].Key < rows[j].Key
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if order == "desc" {
			return less(j, i)
		}
		return less(i, j)
	})
	stats := map[string]*titleStatsJSON{}
	if want["stats"] {
		stats = titleStats(f)
	}
	out := []appTitleJSON{}
	for i := start; i < len(rows) && len(out) < limit; i++ {
		v := rows[i]
		t := f.Titles[v.Key]
		ref, _ := store.ParseTitleKey(v.Key)
		j := appTitleJSON{Kind: ref.Kind, TMDB: ref.TMDB(), IMDb: t.IMDb, TVDB: t.TVDB}
		if ref.Kind == "season" {
			j.Season = &ref.Season
		}
		if want["score"] {
			j.Score = &v.Score
		}
		if want["stats"] {
			j.Stats = stats[v.Key]
			if j.Stats == nil {
				j.Stats = &titleStatsJSON{}
			}
		}
		if want["availability"] {
			j.InLibrary = &v.InLibrary
		}
		out = append(out, j)
	}
	page := map[string]any{"titles": out, "generatedAt": p.clock().UTC().Format(time.RFC3339)}
	if next := start + len(out); next < len(rows) {
		page["next"] = strconv.Itoa(next)
	}
	writeJSON(w, http.StatusOK, page)
}

// titleStats totals, per film and season, what every profile did: plays
// to the end, viewers (accounts), favorites (profiles; a series' count for
// each of its seasons), the day last played. Counts below 2 become 0.
func titleStats(f *store.ValuationFacts) map[string]*titleStatsJSON {
	type total struct {
		plays     int
		viewers   map[string]bool
		favorites int
		last      time.Time
	}
	totals := map[string]*total{}
	get := func(key string) *total {
		t := totals[key]
		if t == nil {
			t = &total{viewers: map[string]bool{}}
			totals[key] = t
		}
		return t
	}
	for _, v := range f.Viewings {
		ref, ok := store.ParseTitleKey(v.Title)
		if !ok {
			continue
		}
		key := ref.Key()
		if ref.Kind == "episode" {
			key = ref.SeasonOf().Key()
		}
		if f.Titles[key] == nil {
			continue
		}
		t := get(key)
		t.plays += v.PlayCount
		if p, ok := f.Profiles[v.ProfileID]; ok {
			t.viewers[p.AccountID] = true
		}
		if v.LastPlayed.After(t.last) {
			t.last = v.LastPlayed
		}
	}
	seasons := map[string][]string{} // series key → its valued seasons
	for key := range f.Titles {
		if ref, _ := store.ParseTitleKey(key); ref.Kind == "season" {
			seasons[ref.Series().Key()] = append(seasons[ref.Series().Key()], key)
		}
	}
	for _, fav := range f.Favorites {
		if f.Titles[fav.Title] != nil {
			get(fav.Title).favorites++
		}
		for _, key := range seasons[fav.Title] {
			get(key).favorites++
		}
	}
	atLeast2 := func(n int) int {
		if n < 2 {
			return 0
		}
		return n
	}
	out := make(map[string]*titleStatsJSON, len(totals))
	for key, t := range totals {
		s := &titleStatsJSON{Plays: atLeast2(t.plays), Viewers: atLeast2(len(t.viewers)), Favorites: atLeast2(t.favorites)}
		if !t.last.IsZero() && len(t.viewers) >= 2 {
			s.LastPlayed = t.last.UTC().Format(time.DateOnly)
		}
		out[key] = s
	}
	return out
}
