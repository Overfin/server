package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/api"
	"github.com/overfin/server/internal/store"
)

type wishlistOut struct {
	On    bool
	Items []struct {
		Title       struct{ ID string }
		State       string
		ReleaseDate string
	}
	Arrived []struct{ ID string }
}

func (w wishlistOut) ids() []string {
	var out []string
	for _, it := range w.Items {
		out = append(out, it.Title.ID)
	}
	return out
}

// A wishlist: films, whole series and seasons no library has, in the
// person's order, never episodes; the owner can switch a profile's
// wishing off; a title that arrives leaves it for the watchlist.
func TestWishlist(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{store: &st, wishes: true})
	ctx := context.Background()
	det, _ := json.Marshal(store.TitleDetails{Seasons: []store.SeasonInfo{{Number: 1, EpisodeCount: 1}, {Number: 2, EpisodeCount: 1}}})
	st.SetTitleDetails(ctx, store.Title{Kind: "series", TMDB: "1399", Name: "Le Trône de fer"}, det, time.Now())
	st.SetSeason(ctx, "1399", store.Season{Number: 1, Name: "Saison 1", AirDate: "2011-04-17", Episodes: []store.Episode{{Number: 1, AirDate: "2011-04-17"}}}, time.Now())
	st.SetSeason(ctx, "1399", store.Season{Number: 2, Name: "Saison 2", AirDate: "2012-04-01", Episodes: []store.Episode{{Number: 1, AirDate: "2012-04-01"}}}, time.Now())
	st.SetTitleRatings(ctx, store.RatedTitle{Kind: "series", TMDB: "1399"}, nil, "2011-04-17", "2011-04-17", time.Now())
	st.SetTitleRatings(ctx, store.RatedTitle{Kind: "movie", TMDB: "100"}, nil, "2024-01-01", "2024-03-01", time.Now())
	film, _ := json.Marshal(store.TitleDetails{})
	st.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "100", Name: "Pas ici"}, film, time.Now())

	var me struct{ Offers struct{ Wishes bool } }
	get(t, srv, "/api/v1/me", &me)
	if !me.Offers.Wishes {
		t.Fatal("wishes not offered")
	}
	for id, want := range map[string]int{
		"title_movie_tmdb_100": 204, "title_series_tmdb_1399": 204, "title_series_tmdb_1399_s2": 204,
		"title_series_tmdb_1399_s1_e1": 400, // never an episode
		"title_movie_tmdb_555":         404, // unknown
	} {
		if code, body := put(t, srv, "PUT", "/api/v1/me/wishlist/"+id, ""); code != want {
			t.Errorf("wish %s: %d %s", id, code, body)
		}
	}
	list := func() wishlistOut {
		t.Helper()
		var out wishlistOut
		if code := get(t, srv, "/api/v1/me/wishlist", &out); code != 200 {
			t.Fatalf("list: %d", code)
		}
		return out
	}
	// Added in the order they came, waiting. The map above isn't ordered:
	// put them in a known order first.
	order := `{"titles":["title_series_tmdb_1399_s2","title_movie_tmdb_100","title_series_tmdb_1399"]}`
	if code, body := put(t, srv, "PUT", "/api/v1/me/wishlist", order); code != 204 {
		t.Fatalf("order: %d %s", code, body)
	}
	if got := list(); !got.On || strings.Join(got.ids(), " ") != "title_series_tmdb_1399_s2 title_movie_tmdb_100 title_series_tmdb_1399" ||
		got.Items[0].State != "waiting" {
		t.Fatalf("ordered: %+v", got)
	}
	if code, _ := put(t, srv, "PUT", "/api/v1/me/wishlist", `{"titles":["title_movie_tmdb_100"]}`); code != 400 {
		t.Errorf("an order missing titles: %d", code)
	}
	// What the fetcher said shows.
	st.SetWishState(ctx, store.WishState{Title: "movie/tmdb/100", State: store.WishUnreleased, ReleaseOn: "2027-01-01"})
	if got := list(); got.Items[1].State != "unreleased" || got.Items[1].ReleaseDate != "2027-01-01" {
		t.Errorf("a state: %+v", got.Items[1])
	}
	if code, _ := put(t, srv, "DELETE", "/api/v1/me/wishlist/title_series_tmdb_1399", ""); code != 204 {
		t.Errorf("remove: %d", code)
	}
	if got := list(); len(got.Items) != 2 {
		t.Fatalf("after a removal: %+v", got)
	}

	// The owner switches another profile's wishing off: it can't wish, and
	// says so; its switch is the owner's alone.
	code, body := post(t, srv, "/api/v1/profiles", `{"name":"Partner"}`)
	var partner struct{ ID string }
	json.Unmarshal([]byte(body), &partner)
	if code != 201 {
		t.Fatalf("partner: %d %s", code, body)
	}
	if code, body := put(t, srv, "PUT", "/api/v1/profiles/"+partner.ID+"/wishing", `{"on":false}`); code != 200 || !strings.Contains(body, `"on":false`) {
		t.Fatalf("switch off: %d %s", code, body)
	}
	if code, body := post(t, srv, "/api/v1/profiles/"+partner.ID+"/activate", `{}`); code != 200 {
		t.Fatalf("activate: %d %s", code, body)
	}
	if code, body := put(t, srv, "PUT", "/api/v1/me/wishlist/title_movie_tmdb_100", ""); code != 403 || !strings.Contains(body, "wishes-off") {
		t.Errorf("wishing while off: %d %s", code, body)
	}
	if got := list(); got.On {
		t.Errorf("listed as on: %+v", got)
	}
	if code, _ := put(t, srv, "PUT", "/api/v1/profiles/"+partner.ID+"/wishing", `{"on":true}`); code != 403 {
		t.Errorf("not the owner switching: %d", code)
	}
	var mine struct{ ID string }
	_, profiles := getAs(t, srv, subject, "/api/v1/profiles")
	var all struct{ Items []struct{ ID, Name string } }
	json.Unmarshal([]byte(profiles), &all)
	for _, p := range all.Items {
		if p.ID != partner.ID {
			mine.ID = p.ID
		}
	}
	if code, body := post(t, srv, "/api/v1/profiles/"+mine.ID+"/activate", `{}`); code != 200 {
		t.Fatalf("back to the owner: %d %s", code, body)
	}

	// The film arrives: it leaves the wishlist, goes on the watchlist, and
	// is listed once as arrived.
	st.PutCatalog(ctx, []store.CatalogItem{{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "Pas ici", SortName: "p", TMDB: "100"}}, 1)
	st.RefreshTitleKeys(ctx)
	var watched []string
	arrivals := &api.WishArrivals{Store: st, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Watch: func(_ context.Context, profileID, titleID string) error {
			watched = append(watched, profileID+" "+titleID)
			return nil
		}}
	if err := arrivals.Settle(ctx); err != nil {
		t.Fatal(err)
	}
	if len(watched) != 1 || watched[0] != mine.ID+" title_movie_tmdb_100" {
		t.Errorf("put on the watchlist: %v", watched)
	}
	got := list()
	if strings.Join(got.ids(), " ") != "title_series_tmdb_1399_s2" || len(got.Arrived) != 1 || got.Arrived[0].ID != "title_movie_tmdb_100" {
		t.Fatalf("after it arrived: %+v", got)
	}
	if code, _ := post(t, srv, "/api/v1/me/wishlist/seen", ``); code != 204 {
		t.Errorf("seen: %d", code)
	}
	if got := list(); len(got.Arrived) != 0 {
		t.Errorf("told twice: %+v", got.Arrived)
	}
	// It's here now: it can't be wished for again.
	if code, body := put(t, srv, "PUT", "/api/v1/me/wishlist/title_movie_tmdb_100", ""); code != 409 || !strings.Contains(body, "already-requested") {
		t.Errorf("wishing for what's here: %d %s", code, body)
	}
}
