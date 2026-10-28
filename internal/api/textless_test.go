package api_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

// fakeTextless is TMDB with text-free posters: Heat (949) has one.
type fakeTextless struct {
	fakePeople
	asked int
}

func (f *fakeTextless) TextlessPoster(_ context.Context, kind string, id int) (string, error) {
	f.asked++
	if kind == "movie" && id == 949 {
		return "/heattextless.jpg", nil
	}
	return "", nil
}

// A title's text-free poster is asked of TMDB the first time its page is,
// when its artwork was kept without it, and never again: TMDB's none
// included.
func TestTextlessAskedOnDemand(t *testing.T) {
	var st *store.Store
	src := &fakeTextless{}
	srv, _ := newServerWith(t, options{store: &st, tmdb: src})
	ctx := context.Background()
	now := time.Now()
	lib := fmt.Sprintf("%032x", 0xf1)
	st.PutCatalog(ctx, []store.CatalogItem{
		{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "949", LibraryID: lib},
		{ID: fmt.Sprintf("%032x", 2), Kind: "movie", Name: "Collateral", SortName: "collateral", TMDB: "1538", LibraryID: lib},
	}, 1)
	st.RefreshTitleKeys(ctx)
	st.SetCatalogState(ctx, store.CatalogState{Pass: 1})
	for _, f := range []struct{ tmdb, name string }{{"949", "Heat"}, {"1538", "Collateral"}} {
		st.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: f.tmdb, Name: f.name, Year: 2000}, []byte(`{}`), now)
		// Kept before text-free posters came with a title's page.
		target := store.ArtworkTarget{Kind: "movie", TMDB: f.tmdb}
		st.SetArtwork(ctx, target, map[string]map[string]string{target.Key(): {"poster": "/frposter12.jpg"}}, now)
	}
	textless := func(id string) string {
		t.Helper()
		var page struct {
			Images struct{ Textless *struct{ URL string } }
		}
		if code := get(t, srv, "/api/v1/titles/title_movie_tmdb_"+id, &page); code != 200 {
			t.Fatalf("%s: %d", id, code)
		}
		if page.Images.Textless == nil {
			return ""
		}
		return page.Images.Textless.URL
	}
	if got := textless("949"); got != "/api/v1/images/tmdbart_poster_heattextless_jpg" {
		t.Errorf("Heat: %q", got)
	}
	if got := textless("1538"); got != "" {
		t.Errorf("Collateral (none): %q", got)
	}
	asked := src.asked
	textless("949")
	textless("1538")
	if src.asked != asked || asked != 2 {
		t.Errorf("TMDB asked %d times, then %d", asked, src.asked)
	}
}
