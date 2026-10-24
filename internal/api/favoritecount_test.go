package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

// A title's page counts the profiles that marked it a favorite, the
// caller's included, but only once two others did: a count never points
// at one person.
func TestFavoriteCount(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{store: &st})
	ctx := context.Background()
	st.PutCatalog(ctx, []store.CatalogItem{{ID: fmt.Sprintf("%032x", 3), Kind: "movie", Name: "Heat", SortName: "Heat", TMDB: "949"}}, 1)
	st.RefreshTitleKeys(ctx)
	count := func() int {
		t.Helper()
		var it struct{ FavoriteCount int }
		if code := get(t, srv, "/api/v1/titles/title_movie_tmdb_949", &it); code != 200 {
			t.Fatalf("title: %d", code)
		}
		return it.FavoriteCount
	}
	other := func(n int) {
		_, p, err := st.CreateAccount(ctx, fmt.Sprintf("other-%d", n), store.NewOwner{Name: "O", JellyfinUserID: strings.Repeat(fmt.Sprint(n), 32),
			JellyfinUsername: fmt.Sprintf("of-o%d", n), JellyfinToken: "t"})
		if err != nil {
			t.Fatal(err)
		}
		st.SetFavorite(ctx, p.ID, "movie/tmdb/949", "", true, time.Now())
	}
	if code, _ := put(t, srv, "PUT", "/api/v1/titles/title_movie_tmdb_949/favorite", `{}`); code != 200 {
		t.Fatalf("mark: %d", code)
	}
	other(1)
	if n := count(); n != 0 {
		t.Errorf("one other: %d", n)
	}
	other(2)
	if n := count(); n != 3 {
		t.Errorf("two others and me: %d", n)
	}
}
