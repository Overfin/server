package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

// The site's categories: listed in the account's language, their titles
// listed like a library's, and with the profile's own limits.
func TestCategoriesListing(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{store: &st})
	ctx := context.Background()
	films, other := fmt.Sprintf("%032x", 0xf1), fmt.Sprintf("%032x", 0xf2)
	var items []store.CatalogItem
	for i := 1; i <= 4; i++ {
		it := store.CatalogItem{ID: fmt.Sprintf("%032x", i), Kind: "movie", Name: fmt.Sprint(i), SortName: fmt.Sprint(i), LibraryID: films}
		if i == 4 {
			it.LibraryID, it.Tags = other, []string{"Noël"}
		}
		items = append(items, it)
	}
	if err := st.PutCatalog(ctx, items, 1); err != nil {
		t.Fatal(err)
	}
	st.RefreshTitleKeys(ctx)
	st.SetCatalogState(ctx, store.CatalogState{Pass: 1})

	var empty struct {
		Items     []struct{ ID string }
		Libraries bool
	}
	if code := get(t, srv, "/api/v1/categories", &empty); code != 200 || len(empty.Items) != 0 || !empty.Libraries {
		t.Errorf("none yet: %d %+v", code, empty)
	}
	xmas := &store.Category{Names: map[string]string{"en": "Christmas", "fr": "Noël"}, Rules: store.CategoryRules{Tags: []string{"noël"}}, Home: true}
	inFilms := &store.Category{Names: map[string]string{"en": "Films"}, Rules: store.CategoryRules{Libraries: []string{films}}}
	st.SaveCategory(ctx, xmas)
	st.SaveCategory(ctx, inFilms)
	st.SetLibrariesShown(ctx, false)

	code, body := getAs(t, srv, subject, "/api/v1/categories")
	if code != 200 || body != `{"items":[{"id":"`+xmas.ID+`","name":"Christmas","home":true},{"id":"`+inFilms.ID+`","name":"Films","home":false}],"libraries":false}`+"\n" {
		t.Errorf("categories: %d %s", code, body)
	}
	ids := func(path string) []string {
		t.Helper()
		var page struct{ Items []struct{ ID string } }
		if code := get(t, srv, path, &page); code != 200 {
			t.Fatalf("%s: %d", path, code)
		}
		var out []string
		for _, it := range page.Items {
			out = append(out, strings.TrimLeft(strings.TrimPrefix(it.ID, "title_item_"), "0"))
		}
		return out
	}
	if got := ids("/api/v1/titles?kind=movie&category=" + xmas.ID); strings.Join(got, ",") != "4" {
		t.Errorf("christmas: %v", got)
	}
	if got := ids("/api/v1/titles?kind=movie&category=" + inFilms.ID); strings.Join(got, ",") != "1,2,3" {
		t.Errorf("films: %v", got)
	}
	for path, want := range map[string]int{
		"/api/v1/titles?kind=movie&category=cat_x":                          400,
		"/api/v1/titles?kind=movie&category=cat_" + strings.Repeat("a", 26): 404,
		"/api/v1/genres?category=" + xmas.ID:                                200,
		"/api/v1/categories?x=1":                                            400,
	} {
		if code := get(t, srv, path, nil); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}
}

// A profile limited to categories sees only their titles, and nothing no
// library has; the owner sets it like libraries, and a deleted category no
// longer counts.
func TestProfileLimitedToCategories(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{store: &st})
	ctx := context.Background()
	var items []store.CatalogItem
	for i := 1; i <= 3; i++ {
		it := store.CatalogItem{ID: fmt.Sprintf("%032x", i), Kind: "movie", Name: fmt.Sprint(i), SortName: fmt.Sprint(i), LibraryID: fmt.Sprintf("%032x", 0xf1)}
		if i == 3 {
			it.Tags = []string{"Noël"}
		}
		items = append(items, it)
	}
	st.PutCatalog(ctx, items, 1)
	st.RefreshTitleKeys(ctx)
	st.SetCatalogState(ctx, store.CatalogState{Pass: 1})
	xmas := &store.Category{Names: map[string]string{"en": "Christmas"}, Rules: store.CategoryRules{Tags: []string{"noël"}}}
	st.SaveCategory(ctx, xmas)

	code, body := post(t, srv, "/api/v1/profiles", `{"name":"Partner","categories":["cat_`+strings.Repeat("a", 26)+`"]}`)
	if code != 400 {
		t.Errorf("unknown category: %d %s", code, body)
	}
	code, body = post(t, srv, "/api/v1/profiles", `{"name":"Partner","categories":["`+xmas.ID+`"]}`)
	if code != 201 || !strings.Contains(body, `"categories":["`+xmas.ID+`"]`) {
		t.Fatalf("create: %d %s", code, body)
	}
	var partner struct{ ID string }
	json.Unmarshal([]byte(body), &partner)
	post(t, srv, "/api/v1/profiles/"+partner.ID+"/activate", `{}`)
	var page struct{ Items []struct{ ID string } }
	get(t, srv, "/api/v1/titles?kind=movie", &page)
	if len(page.Items) != 1 || !strings.HasSuffix(page.Items[0].ID, "3") {
		t.Errorf("limited: %+v", page.Items)
	}
	if code := get(t, srv, fmt.Sprintf("/api/v1/titles/title_item_%032x", 1), nil); code != 404 {
		t.Errorf("a title outside: %d", code)
	}
	// Its category deleted: nothing, until the owner chooses again.
	st.DeleteCategory(ctx, xmas.ID)
	get(t, srv, "/api/v1/titles?kind=movie", &page)
	if len(page.Items) != 0 {
		t.Errorf("after the category went: %+v", page.Items)
	}
}

// A title's similar titles: those a library has first, then, when the
// operator shows titles no library has on the home page, the others.
func TestSimilarTitles(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{store: &st})
	ctx := context.Background()
	now := time.Now()
	st.PutCatalog(ctx, []store.CatalogItem{
		{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "949", LibraryID: fmt.Sprintf("%032x", 0xf1)},
		{ID: fmt.Sprintf("%032x", 2), Kind: "movie", Name: "Collateral", SortName: "collateral", TMDB: "1538", LibraryID: fmt.Sprintf("%032x", 0xf1)},
	}, 1)
	st.RefreshTitleKeys(ctx)
	st.SetCatalogState(ctx, store.CatalogState{Pass: 1})
	st.PutTitles(ctx, []store.Title{{Kind: "movie", TMDB: "13", Name: "Missing one", Year: 2000}, {Kind: "movie", TMDB: "1538", Name: "Collateral", Year: 2004}}, now)
	st.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "949", Name: "Heat", Year: 1995}, []byte(`{"similar":["13","1538"]}`), now)
	st.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "1538", Name: "Collateral", Year: 2004}, []byte(`{}`), now)
	st.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "13", Name: "Missing one", Year: 2000}, []byte(`{}`), now)
	st.SetTitleRatings(ctx, store.RatedTitle{Kind: "movie", TMDB: "13"}, nil, "2000-01-01", "2000-06-01", now)

	names := func() []string {
		t.Helper()
		var page struct{ Items []struct{ Name string } }
		if code := get(t, srv, "/api/v1/titles/title_movie_tmdb_949/similar", &page); code != 200 {
			t.Fatalf("similar: %d", code)
		}
		var out []string
		for _, it := range page.Items {
			out = append(out, it.Name)
		}
		return out
	}
	if got := names(); strings.Join(got, ",") != "Collateral" {
		t.Errorf("library only: %v", got)
	}
	st.SetHomeMissing(ctx, true)
	if got := names(); strings.Join(got, ",") != "Collateral,Missing one" {
		t.Errorf("with titles no library has: %v", got)
	}
	if code := get(t, srv, "/api/v1/titles/title_movie_tmdb_1538/similar", nil); code != 200 {
		t.Errorf("none: %d", code)
	}
	if code := get(t, srv, "/api/v1/titles/title_movie_tmdb_999999/similar", nil); code != 404 {
		t.Errorf("unknown title: %d", code)
	}
}
