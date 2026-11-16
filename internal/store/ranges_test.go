package store

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// A file holding two episodes (S01E01-E02) is one item; the index keeps a
// row for the second, pointing to it: both episodes are in the library,
// the second plays the item, finishing marks both. Indexed again without
// its range, or deleted, the row goes.
func TestEpisodeRanges(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	n := func(i int) *int { return &i }
	series := CatalogItem{ID: id(0), Kind: "series", Name: "A", SortName: "a", TMDB: "1"}
	season := CatalogItem{ID: id(1), Kind: "season", Name: "S1", SortName: "s1", SeriesID: id(0), SeasonNumber: n(1)}
	file := CatalogItem{ID: id(2), Kind: "episode", Name: "E1", SortName: "e1", SeriesID: id(0), SeasonID: id(1), SeasonNumber: n(1), EpisodeNumber: n(1), EpisodeNumberEnd: n(2)}
	if err := s.PutCatalog(ctx, []CatalogItem{series, season, file}, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshTitleKeys(ctx); err != nil {
		t.Fatal(err)
	}
	second := rangeID(id(2), 2)
	if got := s.RealItem(ctx, second); got != id(2) {
		t.Errorf("real item: %s", got)
	}
	if got := s.RealItem(ctx, id(2)); got != id(2) {
		t.Errorf("the item itself: %s", got)
	}
	if keys, err := s.RangeKeys(ctx, id(2)); err != nil || !slices.Equal(keys, []string{"series/tmdb/1/s1/e2"}) {
		t.Errorf("range keys: %v %v", keys, err)
	}
	shared, err := s.SharedFiles(ctx, []string{"series/tmdb/1/s1/e1", "series/tmdb/1/s1/e2"})
	if err != nil || len(shared) != 1 || shared["series/tmdb/1/s1/e2"] != "series/tmdb/1/s1/e1" {
		t.Errorf("shared: %v %v", shared, err)
	}
	// Indexed again, the same: the row stays as it was, its key kept and
	// nothing told to paired servers; the file's video is its too.
	var changesBefore, changesAfter int
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM title_changes WHERE title_key = 'series/tmdb/1/s1/e2'`).Scan(&changesBefore)
	file.Video = "1080p · H.264"
	if err := s.PutCatalog(ctx, []CatalogItem{file}, 1); err != nil {
		t.Fatal(err)
	}
	var key, video string
	s.db.QueryRowContext(ctx, `SELECT coalesce(title_key, ''), coalesce(video, '') FROM catalog_items WHERE id = ?`, second).Scan(&key, &video)
	if key != "series/tmdb/1/s1/e2" || video != "1080p · H.264" {
		t.Errorf("the second episode's row indexed again: key %q, video %q", key, video)
	}
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM title_changes WHERE title_key = 'series/tmdb/1/s1/e2'`).Scan(&changesAfter)
	if changesAfter != changesBefore {
		t.Errorf("the second episode told as changed again: %d changes, %d before", changesAfter, changesBefore)
	}
	// Indexed again as one episode: the row goes.
	file.EpisodeNumberEnd = nil
	if err := s.PutCatalog(ctx, []CatalogItem{file}, 1); err != nil {
		t.Fatal(err)
	}
	if got := s.RealItem(ctx, second); got != second {
		t.Errorf("row kept: %s", got)
	}
	// Back to two, then the file deleted: the row goes with it.
	file.EpisodeNumberEnd = n(2)
	s.PutCatalog(ctx, []CatalogItem{file}, 1)
	if err := s.DeleteCatalog(ctx, []string{id(2)}); err != nil {
		t.Fatal(err)
	}
	var left int
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM catalog_items WHERE range_of IS NOT NULL`).Scan(&left)
	if left != 0 {
		t.Errorf("%d rows left", left)
	}
}
