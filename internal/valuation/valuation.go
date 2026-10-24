// Package valuation values titles: how much each film and each season is
// worth to the people of a server, from what they watch, mark, keep for
// later and ask for, and from what TMDB says of it. The operator weighs
// the criteria; the result is one score per title, from 0 to 100.
//
// Values never depend on files or sizes: a title is valued the same way
// whether a library has it or not. Nothing in a value says who did what.
package valuation

import (
	"math"
	"sort"
	"time"

	"github.com/overfin/server/internal/store"
)

// The criteria, as the operator's weights name them.
const (
	WatchTime  = "watchTime"
	Audience   = "audience"
	Followed   = "followed"
	Favorites  = "favorites"
	Watchlist  = "watchlist"
	Requested  = "requested"
	Rating     = "rating"
	NewRelease = "newRelease"
	// RecentlyAdded is how recently a title arrived in the libraries, so
	// what just came isn't the least valuable for not being watched yet.
	RecentlyAdded = "recentlyAdded"
	Popularity    = "popularity"
)

// Criteria are every criterion, in the order the control panel shows them.
var Criteria = []string{WatchTime, Audience, Followed, Favorites, Watchlist, Requested, Rating, NewRelease, RecentlyAdded, Popularity}

// people are the criteria that come from what people do; the others come
// from TMDB.
var people = map[string]bool{WatchTime: true, Audience: true, Followed: true, Favorites: true, Watchlist: true, Requested: true}

// MaxWeight is the highest weight; 0 leaves a criterion out.
const MaxWeight = 10

// Defaults are the weights until the operator sets theirs.
func Defaults() map[string]int {
	return map[string]int{WatchTime: 8, Audience: 6, Followed: 6, Favorites: 5, Watchlist: 6, Requested: 6,
		Rating: 5, NewRelease: 3, RecentlyAdded: 5, Popularity: 2}
}

// Weights are saved weights checked and completed: a criterion missing or
// out of range takes its default, an unknown one is dropped.
func Weights(saved map[string]int) map[string]int {
	w := Defaults()
	for _, c := range Criteria {
		if v, ok := saved[c]; ok && v >= 0 && v <= MaxWeight {
			w[c] = v
		}
	}
	return w
}

const (
	// halfLife is how fast viewing fades: half as much after it.
	halfLife = 90 * 24 * time.Hour
	// maxViews is the most views of one title an account counts for.
	maxViews = 3
	// kidsShare is what a kids profile's doings count for (children
	// rewatch).
	kidsShare = 0.5
	// followWindow is how recently an account must have watched a series
	// to be following it.
	followWindow = 90 * 24 * time.Hour
	// ratingPrior is how many votes a rating needs to count for as much as
	// the average of all titles.
	ratingPrior = 50
	// addedFor is how long a title counts as recently added, less and
	// less.
	addedFor = 60 * 24 * time.Hour
	// thinAccounts is how many active accounts it takes for what people do
	// to count half: with fewer, the people's part of every score is
	// pulled toward the middle.
	thinAccounts = 2
)

// Title is a film or a season, with how it ranks on each criterion.
type Title struct {
	Key       string
	Name      string
	Season    int // seasons only
	InLibrary bool
	// Parts are its rank on each criterion that applies to it, from 0 (no
	// sign of it, or the lowest) to 1 (the highest).
	Parts map[string]float64
}

// Measured is every title ranked, as of one moment.
type Measured struct {
	Titles []Title
	// Accounts is how many accounts count (active ones).
	Accounts int
}

// Measure ranks every title of f on every criterion, as of now.
func Measure(f *store.ValuationFacts, now time.Time) *Measured {
	raw := map[string]map[string]float64{} // criterion → title key → measure
	for _, c := range Criteria {
		raw[c] = map[string]float64{}
	}
	active := map[string]float64{} // account → its weight: 1, or kidsShare when only kids profiles
	for _, p := range f.Profiles {
		if p.Active {
			active[p.AccountID] = max(active[p.AccountID], share(p))
		}
	}
	// seasons lists each series' valued seasons, by series key.
	seasons := map[string][]*store.ValuedTitle{}
	for _, t := range f.Titles {
		if r, _ := store.ParseTitleKey(t.Key); r.Kind == "season" {
			seasons[r.Series().Key()] = append(seasons[r.Series().Key()], t)
		}
	}
	// counted collects, per title, the most each account counts for.
	counted := func() map[string]map[string]float64 { return map[string]map[string]float64{} }
	add := func(m map[string]map[string]float64, key, account string, v float64) {
		if m[key] == nil {
			m[key] = map[string]float64{}
		}
		m[key][account] = max(m[key][account], v)
	}

	// Watch time, audience and following, from viewings.
	views := map[string]map[string]float64{} // title → account → views, summed
	audience := counted()
	type follow struct {
		last   time.Time
		season int
		share  float64
	}
	follows := map[string]map[string]follow{} // series → account
	for _, v := range f.Viewings {
		p, ok := f.Profiles[v.ProfileID]
		if !ok || !p.Active {
			continue
		}
		r, ok := store.ParseTitleKey(v.Title)
		if !ok {
			continue
		}
		key := r.Key()
		var length, part float64 = 0, 1
		if r.Kind == "episode" {
			key = r.SeasonOf().Key()
		}
		t := f.Titles[key]
		if t == nil {
			continue
		}
		if r.Kind == "episode" {
			length, part = episodeShare(t, r.Episode)
		} else {
			length = float64(t.RuntimeMs)
		}
		n := float64(v.PlayCount)
		if v.Played && n < 1 {
			n = 1
		}
		if v.PositionMs > 0 && length > 0 {
			n += math.Min(float64(v.PositionMs)/length, 1)
		}
		if n <= 0 {
			continue
		}
		at := v.LastPlayed
		if at.IsZero() {
			at = v.Updated
		}
		fade := math.Pow(0.5, math.Max(now.Sub(at).Hours(), 0)/halfLife.Hours())
		if views[key] == nil {
			views[key] = map[string]float64{}
		}
		views[key][p.AccountID] += n * part * fade * share(p)
		add(audience, key, p.AccountID, share(p))
		if r.Kind == "episode" {
			s := r.Series().Key()
			if follows[s] == nil {
				follows[s] = map[string]follow{}
			}
			fl := follows[s][p.AccountID]
			if at.After(fl.last) {
				fl.last = at
			}
			fl.season = max(fl.season, r.Season)
			fl.share = max(fl.share, share(p))
			follows[s][p.AccountID] = fl
		}
	}
	for key, byAccount := range views {
		for _, n := range byAccount {
			raw[WatchTime][key] += math.Min(n, maxViews)
		}
	}
	sum(raw[Audience], audience)
	followed := counted()
	for series, byAccount := range follows {
		for account, fl := range byAccount {
			if now.Sub(fl.last) > followWindow {
				continue
			}
			// The season they're on and those after it.
			for _, t := range seasons[series] {
				if t.Season >= fl.season && t.Season > 0 {
					add(followed, t.Key, account, fl.share)
				}
			}
		}
	}
	sum(raw[Followed], followed)

	// What people marked, keep for later, and asked for.
	each := func(key string, fn func(t *store.ValuedTitle)) {
		r, ok := store.ParseTitleKey(key)
		if !ok {
			return
		}
		switch r.Kind {
		case "series":
			for _, t := range seasons[r.Key()] {
				fn(t)
			}
		case "episode":
			if t := f.Titles[r.SeasonOf().Key()]; t != nil {
				fn(t)
			}
		default:
			if t := f.Titles[r.Key()]; t != nil {
				fn(t)
			}
		}
	}
	for c, list := range map[string][]store.ProfileTitle{Favorites: f.Favorites, Watchlist: f.Interests} {
		m := counted()
		for _, pt := range list {
			p, ok := f.Profiles[pt.ProfileID]
			if !ok || !p.Active {
				continue
			}
			each(pt.Title, func(t *store.ValuedTitle) { add(m, t.Key, p.AccountID, share(p)) })
		}
		sum(raw[c], m)
	}
	requested := counted()
	for _, a := range f.Requests {
		if active[a.AccountID] == 0 {
			continue
		}
		each(a.Title, func(t *store.ValuedTitle) { add(requested, t.Key, a.AccountID, 1) })
	}
	sum(raw[Requested], requested)

	// What TMDB says: the rating weighted by its votes, so a few votes
	// can't make or break a title; how recent; how popular.
	var total, rated float64
	for _, t := range f.Titles {
		if t.Votes > 0 {
			total += t.Rating
			rated++
		}
	}
	mean := 6.5
	if rated > 0 {
		mean = total / rated
	}
	for _, t := range f.Titles {
		v := float64(t.Votes)
		raw[Rating][t.Key] = (v*t.Rating + ratingPrior*mean) / (v + ratingPrior)
		if !t.Released.IsZero() {
			raw[NewRelease][t.Key] = float64(t.Released.Unix())/86400 + 1e6 // in days, above 0 for any date
		}
		raw[Popularity][t.Key] = t.Popularity
		if !t.Added.IsZero() {
			raw[RecentlyAdded][t.Key] = math.Max(1-math.Max(now.Sub(t.Added).Hours(), 0)/addedFor.Hours(), 0)
		}
	}

	out := &Measured{Titles: make([]Title, 0, len(f.Titles))}
	for _, w := range active {
		if w > 0 {
			out.Accounts++
		}
	}
	ranks := map[string]map[string]float64{}
	for _, c := range Criteria {
		ranks[c] = rank(raw[c])
	}
	for _, t := range f.Titles {
		v := Title{Key: t.Key, Name: t.Name, Season: t.Season, InLibrary: t.InLibrary, Parts: map[string]float64{}}
		for _, c := range Criteria {
			if c == Followed && t.Episodes == nil {
				continue // films aren't followed
			}
			if c == RecentlyAdded && !t.InLibrary {
				continue // what isn't here hasn't arrived
			}
			v.Parts[c] = ranks[c][t.Key]
		}
		out.Titles = append(out.Titles, v)
	}
	sort.Slice(out.Titles, func(i, j int) bool { return out.Titles[i].Key < out.Titles[j].Key })
	return out
}

// share is what a profile's doings count for.
func share(p store.ValuationProfile) float64 {
	if p.Kids {
		return kidsShare
	}
	return 1
}

// sum adds up, per title, what each account counts for.
func sum(dst map[string]float64, m map[string]map[string]float64) {
	for key, byAccount := range m {
		for _, v := range byAccount {
			dst[key] += v
		}
	}
}

// episodeShare is an episode's length (0 when unknown) and its share of its
// season: by length, an episode of unknown length counting as long as the
// season's average.
func episodeShare(t *store.ValuedTitle, episode int) (length, part float64) {
	var known, sum float64
	for _, ms := range t.Episodes {
		if ms > 0 {
			known++
			sum += float64(ms)
		}
	}
	n := float64(len(t.Episodes))
	if n == 0 {
		return 0, 1
	}
	if known == 0 {
		return 0, 1 / n
	}
	avg := sum / known
	length = float64(t.Episodes[episode])
	if length <= 0 {
		length = avg
	}
	return length, length / (sum + (n-known)*avg)
}

// rank turns measures into ranks among the titles that have some: the
// lowest just above 0, the highest 1, equal measures equal ranks. A title
// with no measure (or 0) has 0.
func rank(m map[string]float64) map[string]float64 {
	type kv struct {
		key string
		v   float64
	}
	var list []kv
	for k, v := range m {
		// Rounded, so ten tenths of a view equal one.
		if v = math.Round(v*1e6) / 1e6; v > 0 {
			list = append(list, kv{k, v})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].v < list[j].v })
	out := make(map[string]float64, len(list))
	n := float64(len(list))
	for i := 0; i < len(list); {
		j := i
		for j < len(list) && list[j].v == list[i].v {
			j++
		}
		// Ties share the average of their positions (1-based).
		r := (float64(i+1) + float64(j)) / 2 / n
		for _, e := range list[i:j] {
			out[e.key] = r
		}
		i = j
	}
	return out
}

// Value is a title's score under some weights.
type Value struct {
	*Title
	// Score is from 0 to 100.
	Score float64
}

// Score is t's value under the weights w, with accounts active accounts:
// the weighted average of its ranks, scaled to 100. With few accounts,
// the part that comes from what people do is pulled toward the middle, so
// one person's habits don't make a title the most or the least valuable.
// With every weight at 0, every title scores 50.
func Score(t *Title, w map[string]int, accounts int) float64 {
	var wp, sp, wn, sn float64
	for c, part := range t.Parts {
		weight := float64(w[c])
		if people[c] {
			wp += weight
			sp += weight * part
		} else {
			wn += weight
			sn += weight * part
		}
	}
	if wp+wn == 0 {
		return 50
	}
	if wp > 0 {
		trust := float64(accounts) / float64(accounts+thinAccounts)
		sp = wp * (0.5 + (sp/wp-0.5)*trust)
	}
	return math.Round((sp+sn)/(wp+wn)*1000) / 10
}

// Scored scores every measured title under w, the most valuable first
// (then by key, so the order is stable).
func (m *Measured) Scored(w map[string]int) []Value {
	out := make([]Value, len(m.Titles))
	for i := range m.Titles {
		out[i] = Value{Title: &m.Titles[i], Score: Score(&m.Titles[i], w, m.Accounts)}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}
