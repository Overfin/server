package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/store"
)

type appWishesPage struct {
	Titles []struct {
		Kind, TmdbId, TvdbId, Name string
		Season                     *int
	}
}

// /apps/v1/wishes: the wished titles in turn order, nothing about who, only
// when wishes go to Overswell; a search said back spends the turn of the
// account whose wish it was, a release waited for doesn't.
func TestAppWishes(t *testing.T) {
	ctx := context.Background()
	p, b, db, _ := signedInPanel(t)
	_, ann, _ := db.CreateAccount(ctx, "subject-ann", store.NewOwner{Name: "Ann"})
	_, bob, _ := db.CreateAccount(ctx, "subject-bob", store.NewOwner{Name: "Bob"})
	det, _ := json.Marshal(store.TitleDetails{TVDB: "121361"})
	db.SetTitleDetails(ctx, store.Title{Kind: "series", TMDB: "1399", Name: "Game of Thrones"}, det, time.Now())
	for _, w := range [][2]string{{ann.ID, "movie/tmdb/1"}, {ann.ID, "movie/tmdb/2"}, {bob.ID, "series/tmdb/1399/s2"}, {bob.ID, "movie/tmdb/2"}} {
		if err := db.AddWish(ctx, w[0], w[1]); err != nil {
			t.Fatal(err)
		}
	}
	rec := b.do("POST", "/admin/api/apps", `{"name":"Overswell","scopes":["wishes"]}`, nil)
	var made struct{ Key string }
	if json.Unmarshal(rec.Body.Bytes(), &made); rec.Code != 201 {
		t.Fatalf("key: %d %s", rec.Code, rec.Body)
	}
	app := &browser{h: b.h, cookies: map[string]string{}}
	auth := map[string]string{"Authorization": "Bearer " + made.Key}
	list := func() string {
		t.Helper()
		rec := app.do("GET", "/apps/v1/wishes", "", auth)
		var page appWishesPage
		if json.Unmarshal(rec.Body.Bytes(), &page); rec.Code != 200 {
			t.Fatalf("list: %d %s", rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), ann.ID) || strings.Contains(rec.Body.String(), bob.ID) {
			t.Fatalf("who wished: %s", rec.Body)
		}
		var out []string
		for _, w := range page.Titles {
			s := w.Kind + " " + w.TmdbId
			if w.Season != nil {
				s += " s" + string(rune('0'+*w.Season)) + " " + w.TvdbId
			}
			out = append(out, s)
		}
		return strings.Join(out, ", ")
	}
	// Wishes go nowhere yet: none to fetch.
	if got := list(); got != "" {
		t.Fatalf("not to Overswell: %q", got)
	}
	feats := p.Features.(*memFeatures)
	feats.f.Wishes.To = config.WishesToOverswell
	// Ann's and Bob's accounts in turn (neither had one: by ID), each its
	// first wish; film 2, wished by both, once.
	first, second := "movie 1", "season 1399 s2 121361"
	if bob.AccountID < ann.AccountID {
		first, second = second, first
	}
	if got := list(); got != first+", "+second+", movie 2" {
		t.Fatalf("order: %q", got)
	}
	// A season goes by its series' name.
	rec = app.do("GET", "/apps/v1/wishes", "", auth)
	if !strings.Contains(rec.Body.String(), `"name":"Game of Thrones"`) {
		t.Errorf("a season's name: %s", rec.Body)
	}
	status := func(body string, want int) {
		t.Helper()
		if rec := app.do("POST", "/apps/v1/wishes/status", body, auth); rec.Code != want {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	// The first in line was searched and found: its account's turn is
	// spent, so the other account goes first now.
	firstKind, firstBody := "movie", `{"kind":"movie","tmdbId":"1","state":"downloading"}`
	if strings.HasPrefix(first, "season") {
		firstKind, firstBody = "season", `{"kind":"season","tmdbId":"1399","season":2,"state":"downloading"}`
	}
	status(firstBody, 204)
	if got := list(); !strings.HasPrefix(got, strings.TrimSpace(second)) || strings.Contains(got, first) {
		t.Fatalf("after %s was found: %q", firstKind, got)
	}
	// Not out yet: out of the line, no turn spent.
	turns, _ := db.WishTurns(ctx)
	status(`{"kind":"movie","tmdbId":"2","state":"unreleased","releaseDate":"2027-05-01"}`, 204)
	if after, _ := db.WishTurns(ctx); len(after) != len(turns) {
		t.Errorf("a release waited for spent a turn: %v → %v", turns, after)
	}
	// Film 2, wished by both, due again and found: both accounts spend
	// their turn.
	status(`{"kind":"movie","tmdbId":"2","state":"waiting"}`, 204)
	before, _ := db.WishTurns(ctx)
	status(`{"kind":"movie","tmdbId":"2","state":"downloading"}`, 204)
	after, _ := db.WishTurns(ctx)
	for _, acct := range []string{ann.AccountID, bob.AccountID} {
		if !after[acct].At.After(before[acct].At) {
			t.Errorf("account %s didn't spend its turn: %v → %v", acct, before[acct], after[acct])
		}
	}
	for _, bad := range []string{
		`{"kind":"movie","tmdbId":"3","state":"downloading"}`,                 // no one wishes it
		`{"kind":"movie","tmdbId":"1","state":"lost"}`,                        // not a state
		`{"kind":"episode","tmdbId":"1399","season":2,"state":"downloading"}`, // not a kind
	} {
		want := 400
		if strings.Contains(bad, `"tmdbId":"3"`) {
			want = 404
		}
		status(bad, want)
	}
	// Another scope's key reads nothing here.
	rec = b.do("POST", "/admin/api/apps", `{"name":"Other","scopes":["history"]}`, nil)
	json.Unmarshal(rec.Body.Bytes(), &made)
	if rec := app.do("GET", "/apps/v1/wishes", "", map[string]string{"Authorization": "Bearer " + made.Key}); rec.Code != 401 {
		t.Errorf("history key: %d", rec.Code)
	}
}
