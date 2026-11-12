package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/imdb"
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
	ask := func() {
		t.Helper()
		preview = nil
		rec = b.do("POST", "/admin/api/valuation/preview", `{"weights":{"watchTime":10,"audience":0,"followed":0,"favorites":0,"watchlist":0,"requested":0,"rating":0,"newRelease":0,"popularity":0},"query":"watch"}`, nil)
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &preview) != nil {
			t.Fatalf("preview: %d %s", rec.Code, rec.Body)
		}
	}
	// One account's viewing can't be read from the preview (rule 61): the
	// film it watched scores as the one nobody watched.
	ask()
	if len(preview["most"]) != 2 || preview["most"][0].Score != preview["least"][0].Score {
		t.Errorf("one account: most %+v, least %+v", preview["most"], preview["least"])
	}
	// With a second account watching it, it counts.
	_, prof2, _ := db.CreateAccount(ctx, "person-2", store.NewOwner{Name: "Other", JellyfinUserID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", JellyfinUsername: "of-2", JellyfinToken: "t"})
	db.RecordPlay(ctx, prof2.ID, "movie/tmdb/1", id(0), 0, true, time.Now())
	p.valued.m = nil // measured again, not from a few seconds ago
	ask()
	if len(preview["most"]) != 2 || preview["most"][0].ID != "title_movie_tmdb_1" || preview["least"][0].ID != "title_movie_tmdb_2" ||
		!(preview["most"][0].Score > preview["least"][0].Score) {
		t.Errorf("most %+v, least %+v", preview["most"], preview["least"])
	}
	if len(preview["found"]) != 1 || preview["found"][0].Name != "Watched" {
		t.Errorf("found: %+v", preview["found"])
	}
	// Only the score says what people did; no title shows its activity
	// criteria one by one.
	for _, list := range preview {
		for _, v := range list {
			for c := range v.Parts {
				if valuation.FromPeople(c) {
					t.Errorf("%s shows %s: %v", v.Name, c, v.Parts)
				}
			}
		}
	}
	if !strings.Contains(rec.Body.String(), `"rating"`) || strings.Contains(rec.Body.String(), `"watchTime"`) {
		t.Errorf("preview parts: %s", rec.Body)
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

// IMDb's data: switched in the Valuation view where Overfin can have it,
// and refused where it can't.
func TestValuationIMDbSwitch(t *testing.T) {
	ctx := context.Background()
	p, b, db, _ := signedInPanel(t)
	if rec := b.do("GET", "/admin/api/valuation", "", nil); strings.Contains(rec.Body.String(), `"imdb"`) {
		t.Errorf("without a keeper: %s", rec.Body)
	}
	if rec := b.do("PUT", "/admin/api/valuation", `{"imdb":true}`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("switched on without a keeper: %d", rec.Code)
	}

	p.IMDb = imdb.NewKeeper(t.TempDir(), db.IMDbOn, p.Logger)
	var got valuationJSON
	rec := b.do("GET", "/admin/api/valuation", "", nil)
	if json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.IMDb == nil || got.IMDb.On || got.IMDb.Titles != 0 || got.IMDb.Built != "" {
		t.Fatalf("GET: %s", rec.Body)
	}
	rec = b.do("PUT", "/admin/api/valuation", `{"imdb":true}`, nil)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || !got.IMDb.On {
		t.Errorf("on: %d %s", rec.Code, rec.Body)
	}
	if on, _ := db.IMDbOn(ctx); !on {
		t.Error("not kept")
	}
	if w, _ := db.ValuationWeights(ctx); w != nil {
		t.Errorf("switching IMDb's data changed the weights: %v", w)
	}
	b.do("PUT", "/admin/api/valuation", `{"imdb":false}`, nil)
	if on, _ := db.IMDbOn(ctx); on {
		t.Error("not switched off")
	}
}

// The valued titles get IMDb's ratings by the IDs TMDB gives, never by
// numbers: a film its own; a season its episodes', once at least half are
// matched; until then, its series'.
func TestWithIMDb(t *testing.T) {
	basics := "tconst\ttitleType\tisAdult\tstartYear\tendYear\truntimeMinutes\n" +
		"tt0133093\tmovie\t0\t1999\t\\N\t136\n" +
		"tt0903747\ttvSeries\t0\t2008\t2013\t49\n" +
		"tt0959621\ttvEpisode\t0\t2008\t\\N\t58\n" +
		"tt1054724\ttvEpisode\t0\t2008\t\\N\t48\n" +
		"tt1054725\ttvEpisode\t0\t2008\t\\N\t48\n"
	// IMDb numbers them otherwise (season 9): numbers never count.
	episodes := "tconst\tparentTconst\tseasonNumber\tepisodeNumber\n" +
		"tt0959621\ttt0903747\t9\t1\ntt1054724\ttt0903747\t9\t2\ntt1054725\ttt0903747\t9\t3\n"
	ratings := "tconst\taverageRating\tnumVotes\ntt0133093\t8.7\t2200000\ntt0903747\t9.5\t2300000\n" +
		"tt0959621\t9.0\t300\ntt1054724\t8.0\t100\ntt1054725\t7.0\t100\n"
	var out bytes.Buffer
	if _, err := imdb.Build(&out, strings.NewReader(basics), strings.NewReader(episodes), strings.NewReader(ratings), time.Now()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "imdb")
	os.WriteFile(path, out.Bytes(), 0o600)
	d, err := imdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	f := &store.ValuationFacts{Titles: map[string]*store.ValuedTitle{
		"movie/tmdb/603": {Key: "movie/tmdb/603", TitleIMDb: "tt0133093"},
		// The libraries' IMDb ID doesn't count: TMDB's does.
		"movie/tmdb/604": {Key: "movie/tmdb/604", IMDb: "tt0133093"},
		// Two of three matched: (9.0×300 + 8.0×100) / 400.
		"series/tmdb/1396/s1": {Key: "series/tmdb/1396/s1", Season: 1, TitleIMDb: "tt0903747",
			EpisodesIMDb: []string{"tt0959621", "tt1054724", ""}},
		// One of three: the series' rating.
		"series/tmdb/1396/s2": {Key: "series/tmdb/1396/s2", Season: 2, TitleIMDb: "tt0903747",
			EpisodesIMDb: []string{"tt1054725", "", ""}},
		// None matched, no series ID: nothing.
		"series/tmdb/1397/s1": {Key: "series/tmdb/1397/s1", Season: 1, EpisodesIMDb: []string{"", ""}},
		"movie/tmdb/2":        {Key: "movie/tmdb/2", TitleIMDb: "tt9999999"},
	}}
	withIMDb(f, d)
	for key, want := range map[string][2]float64{
		"movie/tmdb/603": {8.7, 2200000}, "movie/tmdb/604": {0, 0},
		"series/tmdb/1396/s1": {8.75, 400}, "series/tmdb/1396/s2": {9.5, 2300000},
		"series/tmdb/1397/s1": {0, 0}, "movie/tmdb/2": {0, 0},
	} {
		if v := f.Titles[key]; v.IMDbRating != want[0] || float64(v.IMDbVotes) != want[1] {
			t.Errorf("%s: %v from %d", key, v.IMDbRating, v.IMDbVotes)
		}
	}
	withIMDb(f, nil) // no file: nothing changes, nothing breaks
}
