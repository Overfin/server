package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

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
