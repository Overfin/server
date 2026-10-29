package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

type appTitlesPage struct {
	Titles []struct {
		Kind, Name, TmdbId, ImdbId string
		Season                     *int
		Score                      *float64
		InLibrary                  *bool
		Stats                      *struct {
			Plays, Viewers, Favorites int
			LastPlayed                string
		}
	}
	Next string
}

// /apps/v1/titles: one row per film and season, with the field groups the
// key may read and nothing else; filters, sorting and pages; counts below
// 2 given as 0; and an older watch-stats key reads statistics here too.
func TestAppTitles(t *testing.T) {
	ctx := context.Background()
	_, b, db, _ := signedInPanel(t)
	now := time.Now()
	var profs []string
	var firstAcct *store.Account
	for _, s := range []string{"1", "2"} {
		acct, prof, _ := db.CreateAccount(ctx, "person-"+s, store.NewOwner{Name: "Main", JellyfinUserID: strings.Repeat(s, 32), JellyfinUsername: "of-" + s, JellyfinToken: "t"})
		profs = append(profs, prof.ID)
		if firstAcct == nil {
			firstAcct = acct
		}
	}
	// A second profile of the first account: still one person.
	kid, err := db.AddProfile(ctx, firstAcct.ID, store.NewProfile{Name: "Kid", Kind: "standard", JellyfinUserID: strings.Repeat("9", 32), JellyfinUsername: "of-9", JellyfinToken: "t9"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	n := func(i int) *int { return &i }
	db.PutCatalog(ctx, []store.CatalogItem{
		{ID: id(0), Kind: "movie", Name: "Alpha", SortName: "alpha", TMDB: "1", IMDb: "tt1", RuntimeMs: 6_000_000, AddedAt: now},
		{ID: id(1), Kind: "movie", Name: "Beta", SortName: "beta", TMDB: "2", RuntimeMs: 6_000_000, AddedAt: now},
		{ID: id(2), Kind: "series", Name: "Show", SortName: "show", TMDB: "3", TVDB: "33"},
		{ID: id(3), Kind: "episode", Name: "E", SortName: "e", SeriesID: id(2), SeasonNumber: n(1), EpisodeNumber: n(1), RuntimeMs: 1_000_000, AddedAt: now},
	}, 1)
	db.RefreshTitleKeys(ctx)
	for _, p := range profs {
		db.RecordPlay(ctx, p, "movie/tmdb/1", id(0), 0, true, now)
	}
	// One person: three plays of Beta, Show a favorite of two profiles.
	for range 3 {
		db.RecordPlay(ctx, profs[0], "movie/tmdb/2", id(1), 0, true, now)
	}
	db.SetFavorite(ctx, profs[0], "series/tmdb/3", id(2), true, now)
	db.SetFavorite(ctx, kid.ID, "series/tmdb/3", id(2), true, now)

	key := func(body string) string {
		t.Helper()
		rec := b.do("POST", "/admin/api/apps", body, nil)
		var made struct{ Key string }
		if json.Unmarshal(rec.Body.Bytes(), &made); rec.Code != 201 {
			t.Fatalf("create %s: %d %s", body, rec.Code, rec.Body)
		}
		return made.Key
	}
	app := &browser{h: b.h, cookies: map[string]string{}}
	get := func(k, query string, want int) appTitlesPage {
		t.Helper()
		rec := app.do("GET", "/apps/v1/titles"+query, "", map[string]string{"Authorization": "Bearer " + k})
		var page appTitlesPage
		if json.Unmarshal(rec.Body.Bytes(), &page); rec.Code != want {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body)
		}
		return page
	}

	// A score-only key: its score, sorted, and nothing else.
	scoreOnly := key(`{"name":"Overtide","scopes":["score"]}`)
	page := get(scoreOnly, "?inLibrary=true&sort=score&order=asc", 200)
	if len(page.Titles) != 3 {
		t.Fatalf("titles: %+v", page.Titles)
	}
	for i, r := range page.Titles {
		if r.Score == nil || r.Stats != nil || r.InLibrary != nil {
			t.Errorf("row %d: %+v", i, r)
		}
		if i > 0 && *r.Score < *page.Titles[i-1].Score {
			t.Errorf("not in order: %+v", page.Titles)
		}
	}
	get(scoreOnly, "?fields=stats", 403)
	get(scoreOnly, "?sort=size", 400)
	get(scoreOnly, "?colour=blue", 400)
	if p := get(scoreOnly, "?kind=season", 200); len(p.Titles) != 1 || *p.Titles[0].Season != 1 || p.Titles[0].TmdbId != "3" {
		t.Errorf("seasons: %+v", p.Titles)
	}

	// Pages.
	first := get(scoreOnly, "?limit=2", 200)
	if len(first.Titles) != 2 || first.Next != "2" {
		t.Fatalf("first page: %+v", first)
	}
	if rest := get(scoreOnly, "?limit=2&cursor="+first.Next, 200); len(rest.Titles) != 1 || rest.Next != "" {
		t.Errorf("second page: %+v", rest)
	}

	// A discovery list: its titles only, in its order (films and series
	// side by side, by place); a season by its series' place.
	db.SetList(ctx, "trending", "movie", []string{"2", "9", "1"})
	db.SetList(ctx, "trending", "series", []string{"3"})
	trending := get(scoreOnly, "?list=trending", 200)
	if len(trending.Titles) != 3 || trending.Titles[0].TmdbId != "2" || trending.Titles[1].Season == nil ||
		trending.Titles[2].TmdbId != "1" {
		t.Errorf("trending: %+v", trending.Titles)
	}
	if p := get(scoreOnly, "?list=trending&kind=movie&sort=score&order=asc", 200); len(p.Titles) != 2 || *p.Titles[0].Score > *p.Titles[1].Score {
		t.Errorf("trending by score: %+v", p.Titles)
	}
	if p := get(scoreOnly, "?list=popular", 200); len(p.Titles) != 0 {
		t.Errorf("an empty list: %+v", p.Titles)
	}
	get(scoreOnly, "?list=upcoming", 400)
	// Suggestions: only what the libraries lack (here, nothing), in their
	// own order.
	if p := get(scoreOnly, "?list=suggestions", 200); len(p.Titles) != 0 {
		t.Errorf("suggestions of titles all in the libraries: %+v", p.Titles)
	}
	get(scoreOnly, "?list=suggestions&sort=score", 400)
	get(scoreOnly, "?list=suggestions&inLibrary=true", 400)

	// Statistics: totals, counts below 2 as 0, and plays and favorites
	// only when two people account for them; a stats key can't sort by
	// score.
	stats := key(`{"name":"Dashboard","scopes":["stats","availability"]}`)
	get(stats, "?sort=score", 403)
	page = get(stats, "?kind=movie", 200)
	if len(page.Titles) != 2 || page.Titles[0].TmdbId != "1" || page.Titles[0].Name != "Alpha" || page.Titles[0].ImdbId != "tt1" || page.Titles[0].Score != nil {
		t.Fatalf("by name: %+v", page.Titles)
	}
	alpha, beta := page.Titles[0].Stats, page.Titles[1].Stats
	if alpha.Plays != 2 || alpha.Viewers != 2 || alpha.LastPlayed == "" || beta.Plays != 0 || beta.Viewers != 0 || beta.LastPlayed != "" {
		t.Errorf("alpha %+v, beta %+v", alpha, beta)
	}
	if p := get(stats, "?kind=season", 200); len(p.Titles) != 1 || p.Titles[0].Stats.Favorites != 0 {
		t.Errorf("one person's favorite, on two profiles: %+v", p.Titles[0].Stats)
	}
	if !*page.Titles[0].InLibrary {
		t.Errorf("availability: %+v", page.Titles[0])
	}

	// An older key, for watch statistics, reads them here as well.
	old := key(`{"name":"Stewarr","scope":"watch-stats"}`)
	if p := get(old, "", 200); len(p.Titles) != 3 || p.Titles[0].Stats == nil || p.Titles[0].Score != nil {
		t.Errorf("an older key: %+v", p.Titles)
	}
	get(old, "?fields=score", 403)
}

// Plays and the day last played count only accounts that played the
// title: one that only started it doesn't make another's plays anonymous.
func TestAppTitlesPlaysNeedTwoPlayers(t *testing.T) {
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	f := &store.ValuationFacts{
		Profiles: map[string]store.ValuationProfile{"pa": {AccountID: "A"}, "pb": {AccountID: "B"}},
		Titles:   map[string]*store.ValuedTitle{"movie/tmdb/550": {Key: "movie/tmdb/550"}},
		Viewings: []store.Viewing{
			{ProfileID: "pa", Title: "movie/tmdb/550", PlayCount: 7, LastPlayed: day},
			{ProfileID: "pb", Title: "movie/tmdb/550", PositionMs: 1000},
		},
	}
	s := titleStats(f)["movie/tmdb/550"]
	if s.Plays != 0 || s.LastPlayed != "" || s.Viewers != 2 {
		t.Fatalf("one player: plays %d, last %q, viewers %d", s.Plays, s.LastPlayed, s.Viewers)
	}
	f.Viewings[1].PlayCount, f.Viewings[1].LastPlayed = 1, day
	s = titleStats(f)["movie/tmdb/550"]
	if s.Plays != 8 || s.LastPlayed != "2026-10-01" {
		t.Fatalf("two players: plays %d, last %q", s.Plays, s.LastPlayed)
	}
}

// A title the libraries lack, wanted by one account only (asked for,
// kept for later, by any of its profiles), isn't listed: its row alone
// would tell what that person wants. Two accounts' wishes, or one of each
// kind from two accounts, list it.
func TestAppTitlesOneWishStaysUnseen(t *testing.T) {
	ctx := context.Background()
	p, b, db, _ := signedInPanel(t)
	now := time.Now()
	var accts, profs []string
	for _, s := range []string{"1", "2"} {
		acct, prof, _ := db.CreateAccount(ctx, "person-"+s, store.NewOwner{Name: "Main", JellyfinUserID: strings.Repeat(s, 32), JellyfinUsername: "of-" + s, JellyfinToken: "t"})
		accts, profs = append(accts, acct.ID), append(profs, prof.ID)
	}
	kid, err := db.AddProfile(ctx, accts[0], store.NewProfile{Name: "Kid", Kind: "standard", JellyfinUserID: strings.Repeat("9", 32), JellyfinUsername: "of-9", JellyfinToken: "t9"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	ask := func(acct, prof, title string) {
		t.Helper()
		if err := db.AddRequest(ctx, &store.Request{AccountID: acct, ProfileID: prof, Title: title, Status: "pending", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	ask(accts[0], profs[0], "movie/tmdb/10") // one account
	ask(accts[0], profs[0], "movie/tmdb/12") // asked for by one,
	ask(accts[0], profs[0], "movie/tmdb/13") // asked for by both
	ask(accts[1], profs[1], "movie/tmdb/13")
	p.Interests = func(context.Context) ([]store.ProfileTitle, error) {
		return []store.ProfileTitle{
			{ProfileID: profs[0], Title: "movie/tmdb/11"}, // two profiles, one account
			{ProfileID: kid.ID, Title: "movie/tmdb/11"},
			{ProfileID: profs[1], Title: "movie/tmdb/12"}, // kept for later by the other
		}, nil
	}

	rec := b.do("POST", "/admin/api/apps", `{"name":"Overtide","scopes":["score"]}`, nil)
	var made struct{ Key string }
	if json.Unmarshal(rec.Body.Bytes(), &made); rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	app := &browser{h: b.h, cookies: map[string]string{}}
	for _, query := range []string{"", "?inLibrary=false"} {
		rec := app.do("GET", "/apps/v1/titles"+query, "", map[string]string{"Authorization": "Bearer " + made.Key})
		var page appTitlesPage
		if json.Unmarshal(rec.Body.Bytes(), &page); rec.Code != 200 {
			t.Fatalf("%q: %d %s", query, rec.Code, rec.Body)
		}
		listed := map[string]bool{}
		for _, r := range page.Titles {
			listed[r.TmdbId] = true
		}
		if listed["10"] || listed["11"] || !listed["12"] || !listed["13"] {
			t.Errorf("%q: listed %v, want 12 and 13 only", query, listed)
		}
	}
}
