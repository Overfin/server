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
		Kind, TmdbId, ImdbId string
		Season               *int
		Score                *float64
		InLibrary            *bool
		Stats                *struct {
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
	for _, s := range []string{"1", "2"} {
		_, prof, _ := db.CreateAccount(ctx, "person-"+s, store.NewOwner{Name: "Main", JellyfinUserID: strings.Repeat(s, 32), JellyfinUsername: "of-" + s, JellyfinToken: "t"})
		profs = append(profs, prof.ID)
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
	db.RecordPlay(ctx, profs[0], "movie/tmdb/2", id(1), 0, true, now)
	db.SetFavorite(ctx, profs[0], "series/tmdb/3", id(2), true, now)

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

	// Statistics: totals, counts below 2 as 0; a stats key can't sort by
	// score.
	stats := key(`{"name":"Dashboard","scopes":["stats","availability"]}`)
	get(stats, "?sort=score", 403)
	page = get(stats, "?kind=movie", 200)
	if len(page.Titles) != 2 || page.Titles[0].TmdbId != "1" || page.Titles[0].ImdbId != "tt1" || page.Titles[0].Score != nil {
		t.Fatalf("by name: %+v", page.Titles)
	}
	alpha, beta := page.Titles[0].Stats, page.Titles[1].Stats
	if alpha.Plays != 2 || alpha.Viewers != 2 || alpha.LastPlayed == "" || beta.Plays != 0 || beta.Viewers != 0 || beta.LastPlayed != "" {
		t.Errorf("alpha %+v, beta %+v", alpha, beta)
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
