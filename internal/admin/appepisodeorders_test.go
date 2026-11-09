package admin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/overfin/server/internal/tmdb"
)

func TestAppEpisodeOrders(t *testing.T) {
	p, b, _, _ := signedInPanel(t)
	app := &browser{h: b.h, cookies: map[string]string{}}
	// A key without the catalog scope is refused: episode orders spend
	// TMDB requests, and the operator ticks who may.
	rec := b.do("POST", "/admin/api/apps", `{"name":"Other","scopes":["score"]}`, nil)
	var other struct{ Key string }
	json.Unmarshal(rec.Body.Bytes(), &other)
	if rec := app.do("GET", "/apps/v1/episode-orders?tmdbId=615", "", map[string]string{"Authorization": "Bearer " + other.Key}); rec.Code != 401 {
		t.Errorf("without the catalog scope: %d", rec.Code)
	}
	rec = b.do("POST", "/admin/api/apps", `{"name":"Overtide","scopes":["score","catalog"]}`, nil)
	var made struct{ Key string }
	if json.Unmarshal(rec.Body.Bytes(), &made); rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	auth := map[string]string{"Authorization": "Bearer " + made.Key}

	if rec := app.do("GET", "/apps/v1/episode-orders?tmdbId=615", "", nil); rec.Code != 401 {
		t.Errorf("without a key: %d", rec.Code)
	}
	if rec := app.do("GET", "/apps/v1/episode-orders?tmdbId=615", "", auth); rec.Code != 503 {
		t.Errorf("without TMDB: %d", rec.Code)
	}
	p.EpisodeOrders = func(_ context.Context, id int) (*tmdb.EpisodeOrders, error) {
		if id != 615 {
			return nil, tmdb.ErrNotFound
		}
		return &tmdb.EpisodeOrders{Seasons: []tmdb.OrderSeason{{Season: 1, Rearranged: true, Episodes: []tmdb.OrderEpisode{{Episode: 1, Name: "Space Pilot 3000"}}}}}, nil
	}
	for query, code := range map[string]int{"tmdbId=abc": 400, "": 400, "tmdbId=-1": 400, "tmdbId=7": 404} {
		if rec := app.do("GET", "/apps/v1/episode-orders?"+query, "", auth); rec.Code != code {
			t.Errorf("%q: %d, want %d", query, rec.Code, code)
		}
	}
	rec = app.do("GET", "/apps/v1/episode-orders?tmdbId=615", "", auth)
	var got tmdb.EpisodeOrders
	if json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != 200 || len(got.Seasons) != 1 || !got.Seasons[0].Rearranged || got.Seasons[0].Episodes[0].Name != "Space Pilot 3000" {
		t.Fatalf("orders: %d %s", rec.Code, rec.Body)
	}
}
