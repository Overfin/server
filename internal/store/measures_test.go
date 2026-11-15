package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The measures count films and seasons over the days asked: what arrived
// and was started or finished since, what left and was wished for after,
// and how much of the library was watched; what's older doesn't count.
func TestLibraryMeasures(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, prof, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	n := func(i int) *int { return &i }
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "series", Name: "A", SortName: "a", TMDB: "1"},
		{ID: id(1), Kind: "season", Name: "S1", SortName: "s1", SeriesID: id(0), SeasonNumber: n(1)},
		{ID: id(2), Kind: "episode", Name: "E1", SortName: "e1", SeriesID: id(0), SeasonID: id(1), SeasonNumber: n(1), EpisodeNumber: n(1)},
		{ID: id(3), Kind: "episode", Name: "E2", SortName: "e2", SeriesID: id(0), SeasonID: id(1), SeasonNumber: n(1), EpisodeNumber: n(2)},
		{ID: id(4), Kind: "movie", Name: "M", SortName: "m", TMDB: "3"},
		{ID: id(5), Kind: "movie", Name: "N", SortName: "n", TMDB: "4"},
	}, 1)
	if err := s.RefreshTitleKeys(ctx); err != nil {
		t.Fatal(err)
	}
	key := func(itemID string) string {
		var k string
		if err := s.db.QueryRowContext(ctx, `SELECT title_key FROM catalog_items WHERE id = ?`, itemID).Scan(&k); err != nil {
			t.Fatal(err)
		}
		return k
	}
	now := time.Now()
	day := func(d int) int64 { return now.AddDate(0, 0, -d).Unix() }
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`DELETE FROM title_history`)
	for _, h := range []struct {
		event, kind, item, tmdb string
		season                  any
		at                      int64
	}{
		{"arrived", "movie", id(4), "3", nil, day(10)},
		{"arrived", "movie", id(5), "4", nil, day(10)},
		{"arrived", "season", id(1), "", 1, day(5)}, // its series' TMDB ID through series_id
		{"arrived", "movie", "old", "5", nil, day(200)},
		{"left", "movie", "gone1", "9", nil, day(20)},  // wished for after
		{"left", "season", "gone2", "8", 2, day(30)},   // its series wished for before
		{"left", "movie", "gone3", "10", nil, day(40)}, // nobody asked
	} {
		var series any
		if h.kind == "season" && h.tmdb == "" {
			series = id(0)
		}
		var tmdb any
		if h.tmdb != "" {
			tmdb = h.tmdb
		}
		exec(`INSERT INTO title_history (event, kind, item_id, series_id, season, tmdb, at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			h.event, h.kind, h.item, series, h.season, tmdb, h.at)
	}
	exec(`INSERT INTO wishes (profile_id, title, position, added_at) VALUES (?, 'movie/tmdb/9', 1, ?), (?, 'series/tmdb/8', 2, ?)`,
		prof.ID, day(10), prof.ID, day(35))
	// M watched to the end yesterday; season 1 of A, both episodes.
	s.RecordPlay(ctx, prof.ID, key(id(4)), id(4), 0, true, now.Add(-24*time.Hour))
	s.RecordPlay(ctx, prof.ID, key(id(2)), id(2), 0, true, now.Add(-3*time.Hour))
	s.RecordPlay(ctx, prof.ID, key(id(3)), id(3), 0, true, now.Add(-2*time.Hour))

	m, err := s.LibraryMeasures(ctx, now, 90)
	if err != nil {
		t.Fatal(err)
	}
	want := Measures{Days: 90, Arrived: 3, Started: 2, Finished: 2, Left: 3, Missed: 1, Library: 3, Watched: 2}
	if m != want {
		t.Errorf("measures = %+v, want %+v", m, want)
	}

	// Halfway through the season: started, not finished.
	exec(`UPDATE profile_titles SET played = 0, play_count = 0, position_ms = 60000 WHERE title = ?`, key(id(3)))
	if m, _ := s.LibraryMeasures(ctx, now, 90); m.Started != 2 || m.Finished != 1 {
		t.Errorf("a season half watched: %+v", m)
	}
}
