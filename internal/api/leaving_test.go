package api_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

// A title held for a last watch shows when it leaves to the profile that
// is partway through it, on its card, its page and Home's list; a series
// carries its season's date; a title nobody of this profile wants shows
// nothing.
func TestLeavingSoon(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{store: &st})
	ctx := context.Background()
	id := func(n int) string { return fmt.Sprintf("%032x", n) }
	one, two := 1, 2
	st.PutCatalog(ctx, []store.CatalogItem{
		{ID: id(1), Kind: "movie", Name: "Heat", SortName: "Heat", TMDB: "949"},
		{ID: id(2), Kind: "movie", Name: "Ronin", SortName: "Ronin", TMDB: "8195"},
		{ID: id(10), Kind: "series", Name: "Dark", SortName: "Dark", TMDB: "70523"},
		{ID: id(11), Kind: "season", Name: "Season 1", SortName: "Season 1", SeriesID: id(10), SeasonNumber: &one},
		{ID: id(12), Kind: "episode", Name: "E1", SortName: "E1", SeriesID: id(10), SeasonID: id(11), SeasonNumber: &one, EpisodeNumber: &one},
		{ID: id(13), Kind: "episode", Name: "E2", SortName: "E2", SeriesID: id(10), SeasonID: id(11), SeasonNumber: &one, EpisodeNumber: &two},
	}, 1)
	st.RefreshTitleKeys(ctx)
	st.SetCatalogState(ctx, store.CatalogState{Pass: 1})
	owner := ownerID(t, srv)
	st.MarkImported(ctx, owner, time.Now())
	var me struct{ ActiveProfile struct{ ID string } }
	get(t, srv, "/api/v1/me", &me)
	prof := me.ActiveProfile.ID
	now := time.Now()
	st.RecordPlay(ctx, prof, "movie/tmdb/949", id(1), 60_000, false, now)     // Heat: partway
	st.RecordPlay(ctx, prof, "series/tmdb/70523/s1/e1", id(12), 0, true, now) // Dark S1: one of two
	wanted, err := st.WantedTitles(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	held, err := st.Depart(ctx, []string{"movie/tmdb/949", "movie/tmdb/8195", "series/tmdb/70523/s1"}, wanted, now)
	if err != nil || len(held) != 2 {
		t.Fatalf("held: %v %v", held, err)
	}

	type page struct {
		Items []struct{ ID, LeavesAt string }
	}
	var leaving page
	if code := get(t, srv, "/api/v1/titles/leaving", &leaving); code != 200 || len(leaving.Items) != 2 {
		t.Fatalf("leaving: %d %+v", code, leaving)
	}
	for _, it := range leaving.Items {
		if at, err := time.Parse(time.RFC3339, it.LeavesAt); err != nil || at.Before(now.Add(store.DepartureNotice-time.Minute)) {
			t.Errorf("%s leaves at %q", it.ID, it.LeavesAt)
		}
	}
	var films page
	get(t, srv, "/api/v1/titles?kind=movie", &films)
	flags := map[string]string{}
	for _, it := range films.Items {
		flags[it.ID] = it.LeavesAt
	}
	if flags["title_movie_tmdb_949"] == "" || flags["title_movie_tmdb_8195"] != "" {
		t.Errorf("film cards: %v", flags)
	}
	var series page
	get(t, srv, "/api/v1/titles?kind=series", &series)
	if len(series.Items) != 1 || series.Items[0].LeavesAt == "" {
		t.Errorf("series card: %+v", series.Items)
	}
	var heat struct{ LeavesAt string }
	if code := get(t, srv, "/api/v1/titles/title_movie_tmdb_949", &heat); code != 200 || heat.LeavesAt == "" {
		t.Errorf("Heat's page: %d %+v", code, heat)
	}
}

// A file holding two episodes: the season lists one row for both, their
// titles joined, each with its own part; playing the second plays the file.
func TestSharedFileRow(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{store: &st})
	ctx := context.Background()
	id := func(n int) string { return fmt.Sprintf("%032x", n) }
	one, two, three := 1, 2, 3
	st.PutCatalog(ctx, []store.CatalogItem{
		{ID: id(10), Kind: "series", Name: "Phineas", SortName: "Phineas", TMDB: "1877"},
		{ID: id(11), Kind: "season", Name: "Season 1", SortName: "Season 1", SeriesID: id(10), SeasonNumber: &one},
		{ID: id(12), Kind: "episode", Name: "Rollercoaster", SortName: "E1", SeriesID: id(10), SeasonID: id(11), SeasonNumber: &one, EpisodeNumber: &one, EpisodeNumberEnd: &two},
		{ID: id(13), Kind: "episode", Name: "Lawn Gnome", SortName: "E3", SeriesID: id(10), SeasonID: id(11), SeasonNumber: &one, EpisodeNumber: &three},
	}, 1)
	st.RefreshTitleKeys(ctx)
	st.SetCatalogState(ctx, store.CatalogState{Pass: 1})
	st.MarkImported(ctx, ownerID(t, srv), time.Now())
	var season struct {
		Episodes []struct {
			ID, Name   string
			EpisodeEnd int
			Parts      []struct{ Name string }
		}
	}
	if code := get(t, srv, "/api/v1/titles/title_series_tmdb_1877_s1", &season); code != 200 {
		t.Fatalf("season: %d", code)
	}
	if len(season.Episodes) != 2 || season.Episodes[0].ID != "title_series_tmdb_1877_s1_e1" || season.Episodes[0].EpisodeEnd != 2 ||
		len(season.Episodes[0].Parts) != 2 || season.Episodes[0].Name == " / " || season.Episodes[1].EpisodeEnd != 0 {
		t.Errorf("episodes: %+v", season.Episodes)
	}
	// Marked watched: both episodes are.
	if code, body := put(t, srv, "PUT", "/api/v1/titles/title_series_tmdb_1877_s1_e1/played", ""); code != 200 {
		t.Fatalf("mark: %d %s", code, body)
	}
	var me struct{ ActiveProfile struct{ ID string } }
	get(t, srv, "/api/v1/me", &me)
	states, err := st.PersonalStates(ctx, me.ActiveProfile.ID, []string{"series/tmdb/1877/s1/e1", "series/tmdb/1877/s1/e2", "series/tmdb/1877/s1/e3"})
	if err != nil || !states["series/tmdb/1877/s1/e1"].Played || !states["series/tmdb/1877/s1/e2"].Played || states["series/tmdb/1877/s1/e3"].Played {
		t.Errorf("played: %+v %v", states, err)
	}
	// Unmarked from the second episode: the first goes with it, as both
	// are one file.
	if code, body := put(t, srv, "DELETE", "/api/v1/titles/title_series_tmdb_1877_s1_e2/played", ""); code != 200 {
		t.Fatalf("unmark: %d %s", code, body)
	}
	states, err = st.PersonalStates(ctx, me.ActiveProfile.ID, []string{"series/tmdb/1877/s1/e1", "series/tmdb/1877/s1/e2"})
	if err != nil || states["series/tmdb/1877/s1/e1"].Played || states["series/tmdb/1877/s1/e2"].Played {
		t.Errorf("unmarked from the second episode: %+v %v", states, err)
	}
}
