package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Who wants what: a watchlist's film, series, season or episode; a film
// started and not finished; a season with an episode watched or started
// and one not finished. A film or season finished is no longer wanted.
func TestWantedTitles(t *testing.T) {
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
	s.RecordPlay(ctx, prof.ID, key(id(4)), id(4), 60_000, false, now) // M started
	s.RecordPlay(ctx, prof.ID, key(id(5)), id(5), 0, true, now)       // N finished
	s.RecordPlay(ctx, prof.ID, key(id(2)), id(2), 0, true, now)       // S1: one of two
	w, err := s.WantedTitles(ctx, []ProfileTitle{{ProfileID: "other", Title: "series/tmdb/9"}})
	if err != nil {
		t.Fatal(err)
	}
	for title, want := range map[string]bool{"movie/tmdb/3": true, "movie/tmdb/4": false, "series/tmdb/1/s1": true, "series/tmdb/1": true,
		"series/tmdb/9/s2": true, "series/tmdb/8": false} {
		if got := w.Anyone(title); got != want {
			t.Errorf("%s wanted: %v", title, got)
		}
	}
	if !w.Wants("series/tmdb/1/s1", prof.ID) || w.Wants("series/tmdb/1/s1", "other") || !w.Wants("series/tmdb/9/s2", "other") {
		t.Errorf("by profile: %v", w)
	}
	// The season finished: no longer wanted.
	s.RecordPlay(ctx, prof.ID, key(id(3)), id(3), 0, true, now)
	if w, _ := s.WantedTitles(ctx, nil); w.Anyone("series/tmdb/1/s1") {
		t.Errorf("finished season still wanted: %v", w)
	}
}

// A title someone wants is held seven days from the first time it's asked
// about, the same date when asked again; then it may go. One nobody wants
// goes at once and loses its hold; one no longer asked about is dropped.
func TestDepart(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	w := Wanted{"movie/tmdb/1": {"p": true}, "series/tmdb/2/s1": {"p": true}}
	held, err := s.Depart(ctx, []string{"movie/tmdb/1", "movie/tmdb/5", "series/tmdb/2"}, w, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 2 || !held["movie/tmdb/1"].Equal(now.Add(DepartureNotice)) || !held["series/tmdb/2"].Equal(now.Add(DepartureNotice)) {
		t.Fatalf("held: %v", held)
	}
	later := now.Add(30 * time.Minute)
	if held, _ := s.Depart(ctx, []string{"movie/tmdb/1"}, w, later); !held["movie/tmdb/1"].Equal(now.Add(DepartureNotice)) {
		t.Errorf("asked again: %v", held)
	}
	if list, _ := s.Departures(ctx, later); len(list) != 2 {
		t.Errorf("held now: %v", list)
	}
	// The series isn't asked about for over an hour: dropped.
	if list, _ := s.Departures(ctx, now.Add(2*time.Hour)); len(list) != 0 {
		t.Errorf("not asked about: %v", list)
	}
	// Seven days on, still asked about: it may go.
	for at := now.Add(time.Hour); at.Before(now.Add(DepartureNotice)); at = at.Add(50 * time.Minute) {
		s.Depart(ctx, []string{"movie/tmdb/1"}, w, at)
	}
	if held, _ := s.Depart(ctx, []string{"movie/tmdb/1"}, w, now.Add(DepartureNotice)); len(held) != 0 {
		t.Errorf("after its date: %v", held)
	}
	// Nobody wants it any more: no hold, and no record.
	s.Depart(ctx, []string{"movie/tmdb/7"}, Wanted{"movie/tmdb/7": {"p": true}}, now)
	s.Depart(ctx, []string{"movie/tmdb/7"}, Wanted{}, now.Add(time.Minute))
	if list, _ := s.Departures(ctx, now.Add(2*time.Minute)); !list["movie/tmdb/7"].IsZero() {
		t.Errorf("unwanted kept: %v", list)
	}
}
