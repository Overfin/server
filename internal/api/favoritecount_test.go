package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

// A title's page never says how many others marked it a favorite (rule
// 44), however many did.
func TestNoFavoriteCount(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{store: &st})
	ctx := context.Background()
	st.PutCatalog(ctx, []store.CatalogItem{{ID: fmt.Sprintf("%032x", 3), Kind: "movie", Name: "Heat", SortName: "Heat", TMDB: "949"}}, 1)
	st.RefreshTitleKeys(ctx)
	if code, _ := put(t, srv, "PUT", "/api/v1/titles/title_movie_tmdb_949/favorite", `{}`); code != 200 {
		t.Fatalf("mark: %d", code)
	}
	for n := 1; n <= 3; n++ {
		_, p, err := st.CreateAccount(ctx, fmt.Sprintf("other-%d", n), store.NewOwner{Name: "O", JellyfinUserID: strings.Repeat(fmt.Sprint(n), 32),
			JellyfinUsername: fmt.Sprintf("of-o%d", n), JellyfinToken: "t"})
		if err != nil {
			t.Fatal(err)
		}
		st.SetFavorite(ctx, p.ID, "movie/tmdb/949", "", true, time.Now())
	}
	var it map[string]any
	if code := get(t, srv, "/api/v1/titles/title_movie_tmdb_949", &it); code != 200 {
		t.Fatalf("title: %d", code)
	}
	for k := range it {
		if strings.Contains(strings.ToLower(k), "count") {
			t.Errorf("the title says %s: %v", k, it[k])
		}
	}
}
