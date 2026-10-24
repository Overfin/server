package valuation

import (
	"strconv"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

var now = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

// facts is a server with one standard profile per account (a1, a2…), all
// active, and the given titles in its libraries.
func facts(accounts int, titles ...*store.ValuedTitle) *store.ValuationFacts {
	f := &store.ValuationFacts{Profiles: map[string]store.ValuationProfile{}, Titles: map[string]*store.ValuedTitle{}}
	for i := 1; i <= accounts; i++ {
		n := strconv.Itoa(i)
		f.Profiles["p"+n] = store.ValuationProfile{AccountID: "a" + n, Active: true}
	}
	for _, t := range titles {
		t.InLibrary = true
		f.Titles[t.Key] = t
	}
	return f
}

func film(id string) *store.ValuedTitle {
	return &store.ValuedTitle{Key: "movie/tmdb/" + id, RuntimeMs: 6_000_000}
}

func season(id string, n, episodes int) *store.ValuedTitle {
	t := &store.ValuedTitle{Key: "series/tmdb/" + id + "/s" + strconv.Itoa(n), Season: n, Episodes: map[int]int64{}}
	for e := 1; e <= episodes; e++ {
		t.Episodes[e] = 2_400_000
	}
	return t
}

func watched(f *store.ValuationFacts, profile, title string, count int, positionMs int64) {
	f.Viewings = append(f.Viewings, store.Viewing{ProfileID: profile, Title: title, PlayCount: count, Played: count > 0,
		PositionMs: positionMs, LastPlayed: now, Updated: now})
}

func parts(t *testing.T, m *Measured, key string) map[string]float64 {
	t.Helper()
	for _, v := range m.Titles {
		if v.Key == key {
			return v.Parts
		}
	}
	t.Fatalf("%s wasn't measured", key)
	return nil
}

// Watch time is in full views: two people watching half of a film count as
// one watching all of another; a season's ten episodes watched once count
// as one view of it, not ten.
func TestWatchTimeInFullViews(t *testing.T) {
	f := facts(4, film("1"), film("2"), season("3", 1, 10), film("4"))
	watched(f, "p1", "movie/tmdb/1", 0, 3_000_000)
	watched(f, "p2", "movie/tmdb/1", 0, 3_000_000)
	watched(f, "p3", "movie/tmdb/2", 1, 0)
	for e := 1; e <= 10; e++ {
		watched(f, "p4", "series/tmdb/3/s1/e"+strconv.Itoa(e), 1, 0)
	}
	watched(f, "p4", "movie/tmdb/4", 2, 0)
	m := Measure(f, now)
	half, whole, season, twice := parts(t, m, "movie/tmdb/1")[WatchTime], parts(t, m, "movie/tmdb/2")[WatchTime],
		parts(t, m, "series/tmdb/3/s1")[WatchTime], parts(t, m, "movie/tmdb/4")[WatchTime]
	if half != whole || whole != season || !(twice > whole) {
		t.Errorf("two halves %v, one whole %v, a season's ten episodes %v, twice %v", half, whole, season, twice)
	}
}

// One account counts for three views of a title at most, so one heavy
// watcher doesn't outweigh several people; a kids profile counts half, and
// an inactive account not at all.
func TestNoOnePersonDecides(t *testing.T) {
	f := facts(5, film("1"), film("2"), film("3"), film("4"))
	watched(f, "p1", "movie/tmdb/1", 40, 0)
	for _, p := range []string{"p2", "p3", "p4", "p5"} {
		watched(f, p, "movie/tmdb/2", 1, 0)
	}
	f.Profiles["k1"] = store.ValuationProfile{AccountID: "a6", Kids: true, Active: true}
	f.Profiles["k2"] = store.ValuationProfile{AccountID: "a7", Kids: true, Active: true}
	watched(f, "k1", "movie/tmdb/3", 1, 0)
	watched(f, "k2", "movie/tmdb/3", 1, 0)
	f.Profiles["gone"] = store.ValuationProfile{AccountID: "a8", Active: false}
	watched(f, "gone", "movie/tmdb/4", 3, 0)
	m := Measure(f, now)
	heavy, many := parts(t, m, "movie/tmdb/1")[WatchTime], parts(t, m, "movie/tmdb/2")[WatchTime]
	if !(many > heavy) {
		t.Errorf("one account 40 times %v, four accounts once %v", heavy, many)
	}
	// Two kids once each: one view, below the heavy watcher's three.
	if kids := parts(t, m, "movie/tmdb/3"); !(kids[WatchTime] > 0 && kids[WatchTime] < heavy) {
		t.Errorf("two kids %v, one account 40 times %v", kids[WatchTime], heavy)
	}
	if gone := parts(t, m, "movie/tmdb/4"); gone[WatchTime] != 0 || gone[Audience] != 0 {
		t.Errorf("an inactive account counted: %v", gone)
	}
	if m.Accounts != 7 {
		t.Errorf("%d active accounts, want 7", m.Accounts)
	}
}

// Favourites, the watchlist and requests count accounts, once each; a
// series' favourite or request counts for each of its seasons, an
// episode on the watchlist for its season.
func TestMarksCountAccounts(t *testing.T) {
	f := facts(3, film("1"), season("2", 1, 3), season("2", 2, 3), film("3"))
	f.Profiles["p1b"] = store.ValuationProfile{AccountID: "a1", Active: true}
	f.Favorites = []store.ProfileTitle{{ProfileID: "p1", Title: "movie/tmdb/1"}, {ProfileID: "p1b", Title: "movie/tmdb/1"},
		{ProfileID: "p2", Title: "series/tmdb/2"}}
	f.Interests = []store.ProfileTitle{{ProfileID: "p3", Title: "series/tmdb/2/s2/e1"}}
	f.Requests = []store.AccountTitle{{AccountID: "a1", Title: "series/tmdb/2"}, {AccountID: "a2", Title: "movie/tmdb/3"}}
	m := Measure(f, now)
	one, s1, s2 := parts(t, m, "movie/tmdb/1"), parts(t, m, "series/tmdb/2/s1"), parts(t, m, "series/tmdb/2/s2")
	if one[Favorites] != s1[Favorites] || s1[Favorites] != s2[Favorites] {
		t.Errorf("favourites: one account's two profiles %v, a series' seasons %v %v", one[Favorites], s1[Favorites], s2[Favorites])
	}
	if s1[Watchlist] != 0 || s2[Watchlist] == 0 {
		t.Errorf("watchlist: %v %v", s1[Watchlist], s2[Watchlist])
	}
	if s1[Requested] == 0 || s1[Requested] != s2[Requested] || parts(t, m, "movie/tmdb/3")[Requested] != s1[Requested] {
		t.Errorf("requests: %v", m.Titles)
	}
	if _, ok := one[Followed]; ok {
		t.Error("a film can't be followed")
	}
}

// Following a series: the season an account is on and those after it,
// while it watched in the last three months.
func TestFollowed(t *testing.T) {
	f := facts(2, season("1", 1, 2), season("1", 2, 2), season("1", 3, 2), season("2", 1, 2))
	watched(f, "p1", "series/tmdb/1/s2/e1", 1, 0)
	f.Viewings = append(f.Viewings, store.Viewing{ProfileID: "p2", Title: "series/tmdb/2/s1/e1", PlayCount: 1,
		Played: true, LastPlayed: now.Add(-200 * 24 * time.Hour)})
	m := Measure(f, now)
	if parts(t, m, "series/tmdb/1/s1")[Followed] != 0 || parts(t, m, "series/tmdb/1/s2")[Followed] == 0 ||
		parts(t, m, "series/tmdb/1/s3")[Followed] == 0 || parts(t, m, "series/tmdb/2/s1")[Followed] != 0 {
		t.Errorf("followed: %v", m.Titles)
	}
}

// A rating from a few votes counts for little: among ordinary titles,
// many votes at 7 beat a handful at 9.
func TestRatingWeightedByVotes(t *testing.T) {
	few, many := film("1"), film("2")
	few.Rating, few.Votes = 9, 3
	many.Rating, many.Votes = 7, 5000
	titles := []*store.ValuedTitle{few, many}
	for _, id := range []string{"3", "4", "5", "6"} {
		o := film(id)
		o.Rating, o.Votes = 6, 1000
		titles = append(titles, o)
	}
	m := Measure(facts(1, titles...), now)
	if !(parts(t, m, "movie/tmdb/2")[Rating] > parts(t, m, "movie/tmdb/1")[Rating]) {
		t.Errorf("ratings: %v", m.Titles)
	}
}

// Ranks: equal measures share a rank, the highest is 1, none is 0.
func TestRank(t *testing.T) {
	r := rank(map[string]float64{"a": 1, "b": 2, "c": 2, "d": 3, "e": 0})
	if r["a"] != 0.25 || r["b"] != 0.625 || r["c"] != 0.625 || r["d"] != 1 || r["e"] != 0 {
		t.Errorf("%v", r)
	}
}

// Scores: weights 0 leave a criterion out, all 0 gives 50, and with few
// accounts the people's part is pulled toward the middle.
func TestScore(t *testing.T) {
	title := &Title{Parts: map[string]float64{WatchTime: 1, Rating: 0}}
	none := map[string]int{}
	if s := Score(title, none, 10); s != 50 {
		t.Errorf("no weights: %v", s)
	}
	onlyRating := map[string]int{Rating: 10}
	if s := Score(title, onlyRating, 10); s != 0 {
		t.Errorf("rating alone: %v", s)
	}
	onlyWatched := map[string]int{WatchTime: 10}
	one, many := Score(title, onlyWatched, 1), Score(title, onlyWatched, 1000)
	if !(one > 50 && one < many && many > 99) {
		t.Errorf("one account %v, a thousand %v", one, many)
	}
}

// Saved weights are checked and completed.
func TestWeights(t *testing.T) {
	w := Weights(map[string]int{WatchTime: 0, Rating: 11, "nonsense": 3})
	if w[WatchTime] != 0 || w[Rating] != Defaults()[Rating] || len(w) != len(Criteria) {
		t.Errorf("%v", w)
	}
}

// An episode's share of its season goes by length.
func TestEpisodeShare(t *testing.T) {
	s := &store.ValuedTitle{Episodes: map[int]int64{1: 1_000, 2: 3_000, 3: 0}}
	if _, p := episodeShare(s, 2); p != 0.5 {
		t.Errorf("a long episode: %v", p)
	}
	if _, p := episodeShare(s, 3); p != 2_000.0/6_000 {
		t.Errorf("one of unknown length: %v", p)
	}
}

// Recently added: what just arrived ranks highest, less and less for two
// months, then not at all; what isn't in the libraries is left out.
func TestRecentlyAdded(t *testing.T) {
	fresh, month, old, away := film("1"), film("2"), film("3"), film("4")
	fresh.Added, month.Added, old.Added = now.Add(-24*time.Hour), now.Add(-30*24*time.Hour), now.Add(-90*24*time.Hour)
	f := facts(1, fresh, month, old)
	f.Titles[away.Key] = away
	m := Measure(f, now)
	a, b, c := parts(t, m, fresh.Key)[RecentlyAdded], parts(t, m, month.Key)[RecentlyAdded], parts(t, m, old.Key)[RecentlyAdded]
	if !(a > b && b > 0 && c == 0) {
		t.Errorf("a day %v, a month %v, three months %v", a, b, c)
	}
	if _, ok := parts(t, m, away.Key)[RecentlyAdded]; ok {
		t.Error("a title not in the libraries counted as added")
	}
}

// Tastes: what an account says it prefers raises the titles that fit it;
// a kids profile wants children's genres without saying so; what it
// watches takes over from what it said as its viewing grows.
func TestTastes(t *testing.T) {
	drama, comedy, family := film("1"), film("2"), film("3")
	drama.Genres, comedy.Genres, family.Genres = []int{18}, []int{35}, []int{10751}
	show := season("4", 1, 10)
	show.Genres = []int{18}

	// Said: series, and comedies.
	f := facts(1, drama, comedy, family, show)
	f.Tastes = map[string]store.Tastes{"a1": {Prefer: "series", Genres: []int{35}}}
	m := Measure(f, now)
	if !(parts(t, m, show.Key)[Tastes] > parts(t, m, drama.Key)[Tastes]) ||
		!(parts(t, m, comedy.Key)[Tastes] > parts(t, m, drama.Key)[Tastes]) {
		t.Errorf("said series and comedies: %v", m.Titles)
	}

	// Nothing said, nothing watched, not asked: no taste at all.
	f = facts(1, drama, comedy, family, show)
	m = Measure(f, now)
	for _, v := range m.Titles {
		if v.Parts[Tastes] != 0 {
			t.Errorf("no taste known, yet %s: %v", v.Key, v.Parts[Tastes])
		}
	}

	// A kids profile: children's genres, without being asked.
	f = facts(1, drama, comedy, family, show)
	f.Profiles["k1"] = store.ValuationProfile{AccountID: "a1", Kids: true, Active: true}
	m = Measure(f, now)
	if !(parts(t, m, family.Key)[Tastes] > parts(t, m, comedy.Key)[Tastes]) {
		t.Errorf("a kids profile: %v", m.Titles)
	}

	// Said films, but watches dramas, as series: what it watches wins.
	other := season("5", 1, 2)
	other.Genres = []int{18}
	f = facts(1, drama, comedy, family, show, other)
	f.Tastes = map[string]store.Tastes{"a1": {Prefer: "movies", Genres: []int{35}}}
	for e := 1; e <= 10; e++ {
		watched(f, "p1", "series/tmdb/4/s1/e"+strconv.Itoa(e), 3, 0)
	}
	m = Measure(f, now)
	if !(parts(t, m, other.Key)[Tastes] > parts(t, m, comedy.Key)[Tastes]) {
		t.Errorf("said films and comedies, watches drama series: %v", m.Titles)
	}
}
