package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Upgrading, what the libraries have arrived at Jellyfin's date: films and
// seasons (a season with its series' IDs), not series or episodes.
func TestHistoryStartsWithTheLibrary(t *testing.T) {
	s := openAt(t, 69)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	added := time.Date(2025, 5, 6, 7, 8, 9, 0, time.UTC)
	for _, row := range []struct {
		id, kind, series, tmdb, tvdb string
		season                       any
	}{
		{id(0), "movie", "", "1", "", nil},
		{id(1), "series", "", "3", "33", nil},
		{id(2), "season", id(1), "", "", 2},
		{id(3), "episode", id(1), "", "", 2},
	} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO catalog_items (id, kind, name, sort_name, rating, seen, tmdb, tvdb, series_id, season_number, added_at)
			VALUES (?, ?, 'x', 'x', '', 1, nullif(?, ''), nullif(?, ''), nullif(?, ''), ?, ?)`, row.id, row.kind, row.tmdb, row.tvdb, row.series, row.season, added.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	events, err := s.TitleHistory(ctx, time.Time{}, "", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events: %+v", events)
	}
	film, season := events[0], events[1]
	if film.Event != "arrived" || film.Kind != "movie" || film.TMDB != "1" || !film.At.Equal(added) {
		t.Errorf("film: %+v", film)
	}
	if season.Kind != "season" || season.TMDB != "3" || season.TVDB != "33" || season.Season == nil || *season.Season != 2 {
		t.Errorf("season: %+v", season)
	}
}
