package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

// /apps/v1/categories lists the operator's categories for any key, and a
// suggestion carries the categories it fits by what TMDB says of it.
func TestAppCategories(t *testing.T) {
	ctx := context.Background()
	_, b, db, _ := signedInPanel(t)
	now := time.Now()
	var accts, profs []string
	for _, s := range []string{"1", "2"} {
		acct, prof, _ := db.CreateAccount(ctx, "person-"+s, store.NewOwner{Name: "Main", JellyfinUserID: strings.Repeat(s, 32), JellyfinUsername: "of-" + s, JellyfinToken: "t"})
		accts, profs = append(accts, acct.ID), append(profs, prof.ID)
	}
	for i := range accts {
		if err := db.AddRequest(ctx, &store.Request{AccountID: accts[i], ProfileID: profs[i], Title: "movie/tmdb/13", Status: "pending", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	db.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "13", Name: "Spirited", Year: 2001, Genres: []store.TitleGenre{{ID: 16, Name: "Animation"}}}, []byte(`{"language":"ja"}`), now)
	films := &store.Category{Names: map[string]string{"en": "Films", "fr": "Films"}, Rules: store.CategoryRules{Kinds: []string{"movie"}}}
	anime := &store.Category{Names: map[string]string{"en": "Anime"}, Rules: store.CategoryRules{Genres: []int{16}, Languages: []string{"ja"}}}
	series := &store.Category{Names: map[string]string{"en": "Series"}, Rules: store.CategoryRules{Kinds: []string{"series"}}}
	for _, c := range []*store.Category{films, anime, series} {
		if err := db.SaveCategory(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	rec := b.do("POST", "/admin/api/apps", `{"name":"Overswell","scopes":["score"]}`, nil)
	var made struct{ Key string }
	if json.Unmarshal(rec.Body.Bytes(), &made); rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	app := &browser{h: b.h, cookies: map[string]string{}}
	auth := map[string]string{"Authorization": "Bearer " + made.Key}

	rec = app.do("GET", "/apps/v1/categories", "", auth)
	var listed struct {
		Categories []struct {
			ID    string
			Names map[string]string
		}
	}
	if json.Unmarshal(rec.Body.Bytes(), &listed); rec.Code != 200 || len(listed.Categories) != 3 || listed.Categories[0].ID != films.ID || listed.Categories[0].Names["fr"] != "Films" {
		t.Fatalf("categories: %d %s", rec.Code, rec.Body)
	}
	if rec := app.do("GET", "/apps/v1/categories", "", nil); rec.Code != 401 {
		t.Errorf("without a key: %d", rec.Code)
	}

	rec = app.do("GET", "/apps/v1/titles?list=suggestions", "", auth)
	var page struct {
		Titles []struct {
			TmdbId     string
			Categories []string
		}
	}
	if json.Unmarshal(rec.Body.Bytes(), &page); rec.Code != 200 || len(page.Titles) != 1 {
		t.Fatalf("suggestions: %d %s", rec.Code, rec.Body)
	}
	if got := strings.Join(page.Titles[0].Categories, ","); got != films.ID+","+anime.ID {
		t.Errorf("categories of the suggestion: %s", got)
	}
	// Other lists don't carry them.
	rec = app.do("GET", "/apps/v1/titles?inLibrary=false", "", auth)
	if strings.Contains(rec.Body.String(), `"categories"`) {
		t.Errorf("categories outside suggestions: %s", rec.Body)
	}
}
