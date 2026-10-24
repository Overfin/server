package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/valuation"
)

// The Valuation view: the weights with their defaults, checked before
// they're kept, reset; and the titles scored under weights not saved yet,
// the libraries' most and least valuable, and any title found by name.
func TestValuation(t *testing.T) {
	ctx := context.Background()
	p, b, db, _ := signedInPanel(t)
	_, prof, _ := db.CreateAccount(ctx, "person-1", store.NewOwner{Name: "Main", JellyfinUserID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", JellyfinUsername: "of-1", JellyfinToken: "t"})
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	db.PutCatalog(ctx, []store.CatalogItem{
		{ID: id(0), Kind: "movie", Name: "Watched", SortName: "watched", TMDB: "1", RuntimeMs: 6_000_000},
		{ID: id(1), Kind: "movie", Name: "Ignored", SortName: "ignored", TMDB: "2", RuntimeMs: 6_000_000},
	}, 1)
	db.RefreshTitleKeys(ctx)
	db.RecordPlay(ctx, prof.ID, "movie/tmdb/1", id(0), 0, true, time.Now())
	p.Interests = func(context.Context) ([]store.ProfileTitle, error) {
		return []store.ProfileTitle{{ProfileID: prof.ID, Title: "movie/tmdb/3"}}, nil
	}

	var got valuationJSON
	rec := b.do("GET", "/admin/api/valuation", "", nil)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body)
	}
	if len(got.Criteria) != len(valuation.Criteria) || got.Accounts != 1 || got.Titles != 3 || got.Max != 10 {
		t.Errorf("GET: %+v", got)
	}
	for _, bad := range []string{`{"weights":{"rating":11}}`, `{"weights":{"rating":-1}}`, `{"weights":{"size":3}}`, `{"other":1}`} {
		if rec := b.do("PUT", "/admin/api/valuation", bad, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("PUT %s: %d", bad, rec.Code)
		}
	}
	if rec := b.do("PUT", "/admin/api/valuation", `{"weights":{"rating":0,"watchTime":10}}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	if w, _ := db.ValuationWeights(ctx); w["rating"] != 0 || w["watchTime"] != 10 || w["audience"] != valuation.Defaults()["audience"] {
		t.Errorf("saved: %v", w)
	}

	var preview map[string][]valuedJSON
	rec = b.do("POST", "/admin/api/valuation/preview", `{"weights":{"watchTime":10,"audience":0,"followed":0,"favorites":0,"watchlist":0,"requested":0,"rating":0,"newRelease":0,"popularity":0},"query":"watch"}`, nil)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &preview) != nil {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body)
	}
	if len(preview["most"]) != 2 || preview["most"][0].ID != "title_movie_tmdb_1" || preview["least"][0].ID != "title_movie_tmdb_2" ||
		!(preview["most"][0].Score > preview["least"][0].Score) {
		t.Errorf("most %+v, least %+v", preview["most"], preview["least"])
	}
	if len(preview["found"]) != 1 || preview["found"][0].Name != "Watched" {
		t.Errorf("found: %+v", preview["found"])
	}
	if rec := b.do("POST", "/admin/api/valuation/preview", `{"weights":{"rating":42}}`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("preview with a bad weight: %d", rec.Code)
	}

	// Asking accounts what they like: off by default, switched alone.
	if got.AskTastes {
		t.Error("asking tastes is on by default")
	}
	if rec := b.do("PUT", "/admin/api/valuation", `{"askTastes":true}`, nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"askTastes":true`) {
		t.Errorf("ask tastes: %d %s", rec.Code, rec.Body)
	}
	if on, _ := db.TastesAsked(ctx); !on {
		t.Error("asking tastes wasn't kept")
	}
	if w, _ := db.ValuationWeights(ctx); w["watchTime"] != 10 {
		t.Errorf("switching asking changed the weights: %v", w)
	}

	if rec := b.do("PUT", "/admin/api/valuation", `{"reset":true}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("reset: %d", rec.Code)
	}
	if w, _ := db.ValuationWeights(ctx); w != nil {
		t.Errorf("after reset: %v", w)
	}
}
