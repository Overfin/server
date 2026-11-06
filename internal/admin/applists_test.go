package admin

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

// /apps/v1/lists: a key with the lists scope sends the operator's lists,
// each sending replacing the last; their titles the libraries lack are
// picks, and come out in the suggestions.
func TestAppLists(t *testing.T) {
	ctx := context.Background()
	_, b, db, _ := signedInPanel(t)
	key := func(body string) string {
		t.Helper()
		rec := b.do("POST", "/admin/api/apps", body, nil)
		var made struct{ Key string }
		if json.Unmarshal(rec.Body.Bytes(), &made); rec.Code != 201 {
			t.Fatalf("create %s: %d %s", body, rec.Code, rec.Body)
		}
		return made.Key
	}
	lists := key(`{"name":"Overtide","scopes":["lists","score"]}`)
	scoreOnly := key(`{"name":"Other","scopes":["score"]}`)
	app := &browser{h: b.h, cookies: map[string]string{}}
	put := func(k, body string, want int) {
		t.Helper()
		if rec := app.do("PUT", "/apps/v1/lists", body, map[string]string{"Authorization": "Bearer " + k}); rec.Code != want {
			t.Fatalf("%s: %d %s, want %d", body, rec.Code, rec.Body, want)
		}
	}
	put(scoreOnly, `{"lists":[]}`, 401)
	for _, bad := range []string{
		`{}`,
		`{"lists":[{"name":"","titles":[]}]}`,
		`{"lists":[{"name":"A","titles":[]},{"name":"A","titles":[]}]}`,
		`{"lists":[{"name":"A","titles":[{"kind":"season","tmdbId":"1"}]}]}`,
		`{"lists":[{"name":"A","titles":[{"kind":"movie","tmdbId":"01"}]}]}`,
		`{"lists":[{"name":"A","titles":[],"colour":"blue"}]}`,
	} {
		put(lists, bad, 400)
	}

	put(lists, `{"lists":[{"name":"Old","titles":[{"kind":"movie","tmdbId":"9"}]}]}`, 204)
	put(lists, `{"lists":[{"name":"Films","titles":[{"kind":"movie","tmdbId":"550"},{"kind":"series","tmdbId":"1399"},{"kind":"movie","tmdbId":"13"},{"kind":"movie","tmdbId":"550"}]},{"name":"Shows","titles":[{"kind":"series","tmdbId":"1399"}]}]}`, 204)
	names, err := db.OperatorListNames(ctx)
	if err != nil || !reflect.DeepEqual(names, []string{"Films", "Shows"}) {
		t.Errorf("names: %v %v", names, err)
	}
	positions, err := db.OperatorListPositions(ctx)
	if want := []map[string]int{{"movie/550": 0, "series/1399": 0, "movie/13": 1}, {"series/1399": 0}}; err != nil || !reflect.DeepEqual(positions, want) {
		t.Errorf("positions: %v %v", positions, err)
	}

	// Once described, a listed film the libraries lack is suggested.
	if err := db.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "550", Name: "Fight Club", Year: 1999}, []byte(`{}`), time.Now()); err != nil {
		t.Fatal(err)
	}
	rec := app.do("GET", "/apps/v1/titles?list=suggestions", "", map[string]string{"Authorization": "Bearer " + lists})
	var page appTitlesPage
	if json.Unmarshal(rec.Body.Bytes(), &page); rec.Code != 200 || len(page.Titles) != 1 || page.Titles[0].TmdbId != "550" {
		t.Errorf("suggestions: %d %+v", rec.Code, page.Titles)
	}

	put(lists, `{"lists":[]}`, 204)
	if positions, err := db.OperatorListPositions(ctx); err != nil || len(positions) != 0 {
		t.Errorf("after clearing: %v %v", positions, err)
	}
}
