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
	history := key(`{"name":"Overtide","scopes":["history"]}`)
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

// The measures: in the panel signed in, and for an app with a history key
// only; totals, nothing per title.
func TestMeasures(t *testing.T) {
	_, b, _, _ := signedInPanel(t)
	rec := b.do("GET", "/admin/api/measures", "", nil)
	var m struct{ Days, Arrived, Library int }
	if json.Unmarshal(rec.Body.Bytes(), &m); rec.Code != 200 || m.Days != 90 {
		t.Fatalf("panel: %d %s", rec.Code, rec.Body)
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
	for scope, want := range map[string]int{"history": 200, "score": 401} {
		rec := app.do("GET", "/apps/v1/measures", "", map[string]string{"Authorization": "Bearer " + key(`{"name":"`+scope+`","scopes":["`+scope+`"]}`)})
		if rec.Code != want {
			t.Errorf("%s key: %d %s", scope, rec.Code, rec.Body)
		}
	}
	if rec := (&browser{h: b.h, cookies: map[string]string{}}).do("GET", "/admin/api/measures", "", nil); rec.Code == 200 {
		t.Errorf("panel signed out: %d", rec.Code)
	}
}

// An app asks which of the titles it plans to remove should wait: one
// someone keeps for later is held a week, the others may go; the answer
// never says who. A history key only; a malformed title is refused.
func TestAppDepartures(t *testing.T) {
	p, b, _, _ := signedInPanel(t)
	p.Interests = func(context.Context) ([]store.ProfileTitle, error) {
		return []store.ProfileTitle{{ProfileID: "p1", Title: "movie/tmdb/1"}, {ProfileID: "p1", Title: "series/tmdb/2"}}, nil
	}
	key := func(scope string) string {
		t.Helper()
		rec := b.do("POST", "/admin/api/apps", `{"name":"`+scope+`","scopes":["`+scope+`"]}`, nil)
		var made struct{ Key string }
		if json.Unmarshal(rec.Body.Bytes(), &made); rec.Code != 201 {
			t.Fatalf("create: %d %s", rec.Code, rec.Body)
		}
		return made.Key
	}
	app := &browser{h: b.h, cookies: map[string]string{}}
	ask := func(k, body string, want int) []departureJSON {
		t.Helper()
		rec := app.do("POST", "/apps/v1/departures", body, map[string]string{"Authorization": "Bearer " + k, "Content-Type": "application/json"})
		var out struct{ Titles []departureJSON }
		if json.Unmarshal(rec.Body.Bytes(), &out); rec.Code != want {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
		return out.Titles
	}
	history := key("history")
	got := ask(history, `{"titles":[{"kind":"movie","tmdbId":"1"},{"kind":"movie","tmdbId":"3"},{"kind":"season","tmdbId":"2","season":1}]}`, 200)
	if len(got) != 3 || got[0].HoldUntil == "" || got[1].HoldUntil != "" || got[2].HoldUntil == "" {
		t.Fatalf("answer: %+v", got)
	}
	if until, _ := time.Parse(time.RFC3339, got[0].HoldUntil); until.Before(time.Now().Add(store.DepartureNotice - time.Minute)) {
		t.Errorf("held until %s", got[0].HoldUntil)
	}
	ask(history, `{"titles":[{"kind":"season","tmdbId":"2"}]}`, 400)
	ask(history, `{"titles":[{"kind":"movie","tmdbId":"x"}]}`, 400)
	ask(key("score"), `{"titles":[]}`, 401)
}
