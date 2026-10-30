package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

type appHistoryPage struct {
	Events []struct {
		Event, Kind, TmdbId, TvdbId, At string
		Season                          *int
	}
	Next string
}

// /apps/v1/history: films and seasons arriving (at Jellyfin's date) and
// leaving (a season keeps its series' IDs, even when the series leaves
// with it); filtered by since and event, in pages; only for a history key.
func TestAppHistory(t *testing.T) {
	ctx := context.Background()
	_, b, db, _ := signedInPanel(t)
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	n := func(i int) *int { return &i }
	old := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	db.PutCatalog(ctx, []store.CatalogItem{
		{ID: id(0), Kind: "movie", Name: "Alpha", SortName: "alpha", TMDB: "1", AddedAt: old},
		{ID: id(2), Kind: "season", Name: "Season 1", SortName: "season 1", SeriesID: id(1), SeasonNumber: n(1), AddedAt: old},
		{ID: id(1), Kind: "series", Name: "Show", SortName: "show", TMDB: "3", TVDB: "33"},
		{ID: id(3), Kind: "episode", Name: "E", SortName: "e", SeriesID: id(1), SeasonNumber: n(1), EpisodeNumber: n(1), AddedAt: old},
	}, 1)
	// Seen again: nothing new arrived.
	db.PutCatalog(ctx, []store.CatalogItem{{ID: id(0), Kind: "movie", Name: "Alpha", SortName: "alpha", TMDB: "1", AddedAt: old}}, 2)
	before := time.Now().Add(-time.Second)
	// The series leaves whole, its row first.
	if err := db.DeleteCatalog(ctx, []string{id(1), id(2), id(3)}); err != nil {
		t.Fatal(err)
	}

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
	get := func(k, query string, want int) appHistoryPage {
		t.Helper()
		rec := app.do("GET", "/apps/v1/history"+query, "", map[string]string{"Authorization": "Bearer " + k})
		var page appHistoryPage
		if json.Unmarshal(rec.Body.Bytes(), &page); rec.Code != want {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body)
		}
		return page
	}
	history := key(`{"name":"Overswell","scopes":["history"]}`)
	all := get(history, "", 200)
	got := []string{}
	for _, e := range all.Events {
		season := ""
		if e.Season != nil {
			season = string(rune('0' + *e.Season))
		}
		got = append(got, e.Event+" "+e.Kind+" "+e.TmdbId+"/"+e.TvdbId+" "+season)
	}
	want := []string{"arrived movie 1/ ", "arrived season 3/33 1", "left season 3/33 1"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("events:\n%v\nwant\n%v", got, want)
	}
	if all.Events[0].At != old.Format(time.RFC3339) {
		t.Errorf("arrived at %s, want Jellyfin's %s", all.Events[0].At, old.Format(time.RFC3339))
	}
	if left := get(history, "?event=left&since="+before.UTC().Format(time.RFC3339), 200); len(left.Events) != 1 || left.Events[0].Event != "left" {
		t.Errorf("left since: %+v", left.Events)
	}
	first := get(history, "?limit=2", 200)
	if len(first.Events) != 2 || first.Next == "" {
		t.Fatalf("first page: %+v", first)
	}
	if rest := get(history, "?limit=2&cursor="+first.Next, 200); len(rest.Events) != 1 || rest.Next != "" {
		t.Errorf("second page: %+v", rest)
	}
	get(history, "?event=gone", 400)
	get(history, "?since=yesterday", 400)
	get(key(`{"name":"Values","scopes":["score"]}`), "", 401)
}
