package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/overfin/server/internal/seerr"
	"github.com/overfin/server/internal/store"
)

// Collections' pictures, and those of their films not out yet, go only to
// a profile that sees them: a film not out yet only through a collection
// it's listed on, within the profile's limits (kids, categories). A text-
// free poster is a poster: the same check.
func TestCollectionImagesAccess(t *testing.T) {
	var st *store.Store
	dir := t.TempDir()
	srv, _ := newServerWith(t, options{store: &st, artworkDir: dir})
	ctx := context.Background()
	now := time.Now()
	lib := fmt.Sprintf("%032x", 0xf1)
	films := []struct {
		tmdb, name, rating, released, poster string
		have                                 bool
		collection                           int
	}{
		{"1", "Heat", "R", "2000-01-01", "/heatposter11.jpg", true, 100},
		{"2", "Bluey film", "G", "2000-01-01", "/blueyposter1.jpg", true, 200},
		{"5", "Future", "G", "2099-01-01", "/futureposter1.jpg", false, 100},
		// Says it's of the saga, but the saga doesn't list it.
		{"7", "Stray future", "G", "2099-01-01", "/strayposter1.jpg", false, 100},
		{"8", "Lone future", "G", "2099-01-01", "/loneposter12.jpg", false, 0},
		{"9", "Adult future", "R", "2099-01-01", "/adultfuture1.jpg", false, 200},
	}
	var items []store.CatalogItem
	for i, f := range films {
		if f.have {
			it := store.CatalogItem{ID: fmt.Sprintf("%032x", i+1), Kind: "movie", Name: f.name, SortName: f.name, TMDB: f.tmdb, LibraryID: lib}
			if f.rating == "G" {
				it.Tags = []string{"Family"}
			}
			items = append(items, it)
		}
	}
	st.PutCatalog(ctx, items, 1)
	st.RefreshTitleKeys(ctx)
	st.SetCatalogState(ctx, store.CatalogState{Pass: 1})
	keep := func(path string) {
		file := filepath.Join(dir, "poster", "w342", path[1:])
		os.MkdirAll(filepath.Dir(file), 0o700)
		os.WriteFile(file, []byte("jpeg"), 0o600)
	}
	for _, f := range films {
		st.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: f.tmdb, Name: f.name, Year: 2000, Poster: f.poster, Collection: f.collection}, []byte(`{}`), now)
		st.SetTitleRatings(ctx, store.RatedTitle{Kind: "movie", TMDB: f.tmdb}, map[string][]string{"US": {f.rating}}, f.released, f.released, now)
		keep(f.poster)
	}
	heat := store.ArtworkTarget{Kind: "movie", TMDB: "1"}
	st.SetArtwork(ctx, heat, map[string]map[string]string{heat.Key(): {"poster": "/heatposter11.jpg", "textless": "/heattextless.jpg"}}, now)
	keep("/heattextless.jpg")
	st.SetCollection(ctx, store.Collection{ID: 100, Name: "Saga", Poster: "/sagaposter1.jpg", Parts: []string{"1", "5"}}, nil, now)
	st.SetCollection(ctx, store.Collection{ID: 200, Name: "Kids saga", Poster: "/kidsposter1.jpg", Parts: []string{"2", "9"}}, nil, now)
	keep("/sagaposter1.jpg")
	keep("/kidsposter1.jpg")

	image := func(path string) int {
		t.Helper()
		return get(t, srv, "/api/v1/images/tmdbart_poster_"+path[1:len(path)-4]+"_jpg", nil)
	}
	expect := func(who string, want map[string]int) {
		t.Helper()
		for path, code := range want {
			if got := image(path); got != code {
				t.Errorf("%s, %s: %d, want %d", who, path, got, code)
			}
		}
	}
	expect("owner", map[string]int{
		"/heatposter11.jpg": 200, "/heattextless.jpg": 200, "/sagaposter1.jpg": 200, "/kidsposter1.jpg": 200,
		"/futureposter1.jpg": 200, "/adultfuture1.jpg": 200,
		// Not out yet, and on no collection's page.
		"/strayposter1.jpg": 404, "/loneposter12.jpg": 404,
	})
	for _, id := range []string{"5", "7", "8", "9"} {
		if code := get(t, srv, "/api/v1/titles/title_movie_tmdb_"+id, nil); code != 404 {
			t.Errorf("owner, a film not out yet (%s): %d", id, code)
		}
	}

	// A kids profile of 6: the kids saga's pictures, never those of the
	// films above its age, out or not.
	code, body := post(t, srv, "/api/v1/profiles", `{"name":"Kid","kind":"kids","maxAge":6}`)
	if code != 201 {
		t.Fatalf("kids profile: %d %s", code, body)
	}
	var kid struct{ ID string }
	json.Unmarshal([]byte(body), &kid)
	post(t, srv, "/api/v1/profiles/"+kid.ID+"/activate", `{}`)
	expect("kid", map[string]int{
		"/blueyposter1.jpg": 200, "/kidsposter1.jpg": 200,
		"/heatposter11.jpg": 404, "/heattextless.jpg": 404, "/sagaposter1.jpg": 404, "/adultfuture1.jpg": 404,
		"/futureposter1.jpg": 404, "/strayposter1.jpg": 404, "/loneposter12.jpg": 404,
	})
	var kids struct{ Items []struct{ Name string } }
	get(t, srv, "/api/v1/collections/collection_200", &kids)
	if len(kids.Items) != 1 || kids.Items[0].Name != "Bluey film" {
		t.Errorf("kid's saga: %+v", kids.Items)
	}

	// A profile limited to a category: nothing no library has.
	family := &store.Category{Names: map[string]string{"en": "Family"}, Rules: store.CategoryRules{Tags: []string{"family"}}}
	st.SaveCategory(ctx, family)
	post(t, srv, "/api/v1/profiles/"+ownerID(t, srv)+"/activate", `{}`)
	code, body = post(t, srv, "/api/v1/profiles", `{"name":"Partner","categories":["`+family.ID+`"]}`)
	if code != 201 {
		t.Fatalf("partner: %d %s", code, body)
	}
	var partner struct{ ID string }
	json.Unmarshal([]byte(body), &partner)
	post(t, srv, "/api/v1/profiles/"+partner.ID+"/activate", `{}`)
	expect("partner", map[string]int{
		"/blueyposter1.jpg": 200, "/kidsposter1.jpg": 200,
		"/heatposter11.jpg": 404, "/heattextless.jpg": 404, "/sagaposter1.jpg": 404, "/adultfuture1.jpg": 404,
		"/futureposter1.jpg": 404,
	})
}

// A film not out yet, listed on a collection's page, can't be requested.
func TestUpcomingNotRequested(t *testing.T) {
	fake := &fakeSeerrRequests{users: map[int]map[string]any{}, requests: map[int]map[string]any{}}
	seerrSrv := httptest.NewServer(fake)
	defer seerrSrv.Close()
	client, _ := seerr.New(seerrSrv.URL, "key")
	var st *store.Store
	srv, _ := newServerWith(t, options{seerr: client, store: &st})
	ctx := context.Background()
	now := time.Now()
	st.PutCatalog(ctx, []store.CatalogItem{{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "1",
		LibraryID: fmt.Sprintf("%032x", 0xf1)}}, 1)
	st.RefreshTitleKeys(ctx)
	st.SetCatalogState(ctx, store.CatalogState{Pass: 1})
	for _, f := range []struct{ tmdb, released string }{{"1", "2000-01-01"}, {"5", "2099-01-01"}} {
		st.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: f.tmdb, Name: "Film " + f.tmdb, Collection: 100}, []byte(`{}`), now)
		st.SetTitleRatings(ctx, store.RatedTitle{Kind: "movie", TMDB: f.tmdb}, nil, f.released, f.released, now)
	}
	st.SetCollection(ctx, store.Collection{ID: 100, Name: "Saga", Parts: []string{"1", "5"}}, nil, now)
	var page struct{ Items []struct{ Upcoming bool } }
	if code := get(t, srv, "/api/v1/collections/collection_100", &page); code != 200 || len(page.Items) != 2 || !page.Items[1].Upcoming {
		t.Fatalf("saga: %d %+v", code, page.Items)
	}
	if code, body := post(t, srv, "/api/v1/requests", `{"titleId":"title_movie_tmdb_5"}`); code != 404 {
		t.Errorf("requesting a film not out yet: %d %s", code, body)
	}
	if len(fake.requests) != 0 {
		t.Errorf("Seerr asked: %v", fake.requests)
	}
}
