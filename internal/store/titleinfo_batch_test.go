package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// Read at once or each on its own, titles come out the same: films with a
// page or without, series with their seasons and episodes, files TMDB
// doesn't know, titles no library has, ratings, ages and artwork, in the
// default language and another.
func TestTitleInfosBatchedSameAsAlone(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	lib := fmt.Sprintf("%032x", 0xf1)
	s.SetRatingsCountry(ctx, "US")
	s.PutCatalog(ctx, []CatalogItem{
		{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "949", LibraryID: lib},
		{ID: fmt.Sprintf("%032x", 2), Kind: "movie", Name: "Heat (4K)", SortName: "heat", TMDB: "949", LibraryID: lib},
		{ID: fmt.Sprintf("%032x", 3), Kind: "movie", Name: "No page", SortName: "no page", TMDB: "5", LibraryID: lib},
		{ID: fmt.Sprintf("%032x", 4), Kind: "series", Name: "GoT", SortName: "got", TMDB: "1399", LibraryID: lib},
		{ID: fmt.Sprintf("%032x", 5), Kind: "episode", Name: "E1", SortName: "e1", SeriesID: fmt.Sprintf("%032x", 4), SeasonNumber: ptr(1), EpisodeNumber: ptr(1), LibraryID: lib},
		{ID: fmt.Sprintf("%032x", 6), Kind: "movie", Name: "Home video", SortName: "home video", LibraryID: lib},
	}, 1)
	s.RefreshTitleKeys(ctx)
	details, _ := json.Marshal(TitleDetails{Genres: []string{"Crime"}, Runtime: 170, Cast: []CastMember{{Name: "Al", TMDB: "1158"}}, Language: "en"})
	s.SetTitleDetails(ctx, Title{Kind: "movie", TMDB: "949", Name: "Heat", Year: 1995, Overview: "LA.", Rating: 8.3, Votes: 7000, Poster: "/p1234567.jpg"}, details, now)
	s.SetTitleDetails(ctx, Title{Kind: "series", TMDB: "1399", Name: "Game of Thrones", Year: 2011}, []byte(`{"seasons":[{"number":1,"episodeCount":10}]}`), now)
	s.SetTitleDetails(ctx, Title{Kind: "movie", TMDB: "13", Name: "Not here", Year: 2001}, []byte(`{}`), now)
	s.SetTitleRatings(ctx, RatedTitle{Kind: "movie", TMDB: "949"}, map[string][]string{"US": {"R", "PG-13"}, "FR": {"12"}}, "1995-12-15", "1996-06-01", now)
	s.SetTitleRatings(ctx, RatedTitle{Kind: "series", TMDB: "1399"}, map[string][]string{"US": {"TV-MA"}}, "2011-04-17", "2011-04-17", now)
	for _, target := range []ArtworkTarget{{Kind: "movie", TMDB: "949"}, {Kind: "series", TMDB: "1399"}} {
		s.SetArtwork(ctx, target, map[string]map[string]string{target.Key(): {"poster": "/art1234567.jpg", "logo": "/logo123456.png", "textless": "/text123456.jpg"}}, now)
	}
	s.SetSeason(ctx, "1399", Season{Number: 1, Name: "Season 1", Episodes: []Episode{{Number: 1, Name: "Winter Is Coming"}}}, now)
	s.SetTitleTexts(ctx, "en-CA", Title{Kind: "movie", TMDB: "949", Name: "Heat (en)"}, []byte(`{}`), "/enlogo1234.png", now)
	s.SetLanguages(ctx, []string{"fr-CA", "en-CA"}, "fr-CA")

	keys := []string{"movie/tmdb/949", "movie/tmdb/5", "series/tmdb/1399", "series/tmdb/1399/s1", "series/tmdb/1399/s1/e1",
		"movie/tmdb/13", "movie/tmdb/404", "item/" + fmt.Sprintf("%032x", 6), "movie/tmdb/949"}
	for _, language := range []string{"", "en-CA"} {
		batched, err := s.titleInfos(ctx, keys, "2026-10-29", language, true)
		if err != nil {
			t.Fatal(err)
		}
		alone, err := s.titleInfos(ctx, keys, "2026-10-29", language, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(batched) != len(alone) {
			t.Fatalf("%q: %d titles batched, %d alone", language, len(batched), len(alone))
		}
		for k, a := range alone {
			if !reflect.DeepEqual(batched[k], a) {
				t.Errorf("%q, %s:\nbatched %+v\nalone   %+v", language, k, batched[k], a)
			}
		}
	}
	// Something was read at all.
	got, _ := s.TitleInfos(ctx, []string{"movie/tmdb/949"}, "2026-10-29", "")
	if h := got["movie/tmdb/949"]; h == nil || h.Name != "Heat" || len(h.Items) != 2 || h.Certification == "" || h.Textless != "/text123456.jpg" {
		t.Errorf("Heat: %+v", h)
	}
}

func ptr(n int) *int { return &n }

// A profile's states, many titles at once: those it touched, by title,
// those it didn't left out; more than one query's worth too.
func TestPersonalStatesMany(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, prof, err := s.CreateAccount(ctx, "subject-1", owner("1"))
	if err != nil {
		t.Fatal(err)
	}
	profile := prof.ID
	now := time.Now()
	var titles []string
	for i := 0; i < batchChunk+50; i++ {
		titles = append(titles, fmt.Sprintf("movie/tmdb/%d", i+1))
	}
	if err := s.SetPlayed(ctx, profile, map[string]string{"movie/tmdb/1": "", fmt.Sprintf("movie/tmdb/%d", batchChunk+10): ""}, true, now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFavorite(ctx, profile, "movie/tmdb/2", "", true, now); err != nil {
		t.Fatal(err)
	}
	got, err := s.PersonalStates(ctx, profile, append(titles, "movie/tmdb/1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || !got["movie/tmdb/1"].Played || !got[fmt.Sprintf("movie/tmdb/%d", batchChunk+10)].Played || !got["movie/tmdb/2"].Favorite {
		t.Errorf("states: %+v", got)
	}
}
