package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

// Collections: those the profile has a film of, each with its films as the
// profile may see them; those not out yet listed, marked, but without a
// page (so they can't be requested); the profile's limits hold throughout.
func TestCollections(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{store: &st})
	ctx := context.Background()
	now := time.Now()
	lib := fmt.Sprintf("%032x", 0xf1)
	films := []struct {
		tmdb, name, rating, released string
		have                         bool
		collection                   int
	}{
		{"1", "Heat", "R", "2000-01-01", true, 100},
		{"2", "Bluey film", "G", "2000-01-01", true, 200},
		{"3", "Missing adult", "R", "2000-01-01", false, 100},
		{"4", "Missing kids", "G", "2000-01-01", false, 200},
		{"5", "Future", "G", "2099-01-01", false, 100},
		{"6", "Unknown release", "G", "", false, 100},
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
	for _, f := range films {
		st.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: f.tmdb, Name: f.name, Year: 2000, Collection: f.collection}, []byte(`{}`), now)
		if f.released != "" {
			st.SetTitleRatings(ctx, store.RatedTitle{Kind: "movie", TMDB: f.tmdb}, map[string][]string{"US": {f.rating}}, f.released, f.released, now)
		}
	}
	st.SetCollection(ctx, store.Collection{ID: 100, Name: "Saga", Overview: "Four films.", Poster: "/sagaposter1.jpg",
		Parts: []string{"1", "3", "5", "6"}}, nil, now)
	st.SetCollection(ctx, store.Collection{ID: 200, Name: "Kids saga", Poster: "/kidsposter1.jpg", Parts: []string{"2", "4", "5"}}, nil, now)
	// None of its films in a library: never listed.
	st.SetCollection(ctx, store.Collection{ID: 300, Name: "Nothing here", Parts: []string{"3", "4"}}, nil, now)

	type collection struct {
		ID, Name, Overview string
		InLibrary, Films   int
		Items              []struct {
			Name     string
			Upcoming bool
		}
	}
	list := func() []string {
		t.Helper()
		var page struct{ Items []collection }
		if code := get(t, srv, "/api/v1/collections", &page); code != 200 {
			t.Fatalf("collections: %d", code)
		}
		var out []string
		for _, c := range page.Items {
			out = append(out, fmt.Sprintf("%s %d/%d", c.Name, c.InLibrary, c.Films))
		}
		return out
	}
	films2 := func(id string) []string {
		t.Helper()
		var c collection
		if code := get(t, srv, "/api/v1/collections/"+id, &c); code != 200 {
			t.Fatalf("%s: %d", id, code)
		}
		var out []string
		for _, it := range c.Items {
			name := it.Name
			if it.Upcoming {
				name += " (upcoming)"
			}
			out = append(out, name)
		}
		return out
	}

	// The owner: by name; films in release order, the one whose release
	// isn't known left out.
	if got := list(); !slices.Equal(got, []string{"Kids saga 1/3", "Saga 1/4"}) {
		t.Errorf("owner's collections: %v", got)
	}
	if got := films2("collection_100"); !slices.Equal(got, []string{"Heat", "Missing adult", "Future (upcoming)"}) {
		t.Errorf("owner's saga: %v", got)
	}
	for _, id := range []string{"collection_300", "collection_999", "collection_0100", "collection_x", "100"} {
		if code := get(t, srv, "/api/v1/collections/"+id, nil); code != 404 {
			t.Errorf("%s: %d", id, code)
		}
	}
	// A film's page names its collection; one not out yet has no page.
	var heat struct{ Collection *struct{ ID, Name string } }
	if code := get(t, srv, "/api/v1/titles/title_movie_tmdb_1", &heat); code != 200 || heat.Collection == nil ||
		heat.Collection.ID != "collection_100" || heat.Collection.Name != "Saga" {
		t.Errorf("Heat's collection: %d %+v", code, heat.Collection)
	}
	if code := get(t, srv, "/api/v1/titles/title_movie_tmdb_5", nil); code != 404 {
		t.Errorf("a film not out yet: %d", code)
	}
	// Shown everything, the panel says: it's a title like any other.
	st.SetTitlesShown(ctx, "all")
	if got := films2("collection_100"); !slices.Equal(got, []string{"Heat", "Missing adult", "Future", "Unknown release"}) {
		t.Errorf("all titles shown: %v", got)
	}
	st.SetTitlesShown(ctx, "released")

	// A kids profile of 6: the collections it has a film of, the films
	// rated for it. The other collection's pictures are refused.
	code, body := post(t, srv, "/api/v1/profiles", `{"name":"Kid","kind":"kids","maxAge":6}`)
	if code != 201 {
		t.Fatalf("kids profile: %d %s", code, body)
	}
	var kid struct{ ID string }
	json.Unmarshal([]byte(body), &kid)
	post(t, srv, "/api/v1/profiles/"+kid.ID+"/activate", `{}`)
	if got := list(); !slices.Equal(got, []string{"Kids saga 1/3"}) {
		t.Errorf("kid's collections: %v", got)
	}
	if got := films2("collection_200"); !slices.Equal(got, []string{"Bluey film", "Missing kids", "Future (upcoming)"}) {
		t.Errorf("kid's saga: %v", got)
	}
	if code := get(t, srv, "/api/v1/collections/collection_100", nil); code != 404 {
		t.Errorf("kid, the other saga: %d", code)
	}
	if code := get(t, srv, "/api/v1/images/tmdbart_poster_sagaposter1_jpg", nil); code != 404 {
		t.Errorf("kid, the other saga's poster: %d", code)
	}
	var bluey struct{ Collection *struct{ ID string } }
	get(t, srv, "/api/v1/titles/title_movie_tmdb_2", &bluey)
	if bluey.Collection == nil || bluey.Collection.ID != "collection_200" {
		t.Errorf("Bluey's collection: %+v", bluey.Collection)
	}

	// A profile limited to a category: its films only, none no library
	// has.
	owner := ownerID(t, srv)
	family := &store.Category{Names: map[string]string{"en": "Family"}, Rules: store.CategoryRules{Tags: []string{"family"}}}
	st.SaveCategory(ctx, family)
	post(t, srv, "/api/v1/profiles/"+owner+"/activate", `{}`)
	code, body = post(t, srv, "/api/v1/profiles", `{"name":"Partner","categories":["`+family.ID+`"]}`)
	if code != 201 {
		t.Fatalf("partner: %d %s", code, body)
	}
	var partner struct{ ID string }
	json.Unmarshal([]byte(body), &partner)
	post(t, srv, "/api/v1/profiles/"+partner.ID+"/activate", `{}`)
	if got := list(); !slices.Equal(got, []string{"Kids saga 1/3"}) {
		t.Errorf("partner's collections: %v", got)
	}
	if got := films2("collection_200"); !slices.Equal(got, []string{"Bluey film"}) {
		t.Errorf("partner's saga: %v", got)
	}
	var heat2 struct{ Collection *struct{ ID string } }
	if code := get(t, srv, "/api/v1/titles/title_movie_tmdb_1", &heat2); code != 404 && heat2.Collection != nil {
		t.Errorf("partner, Heat's collection: %+v", heat2.Collection)
	}
}
