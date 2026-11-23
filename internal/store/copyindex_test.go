package store

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/msfp-io/msfp-go/wire"
)

// changes counts the changes noted of a title key.
func changes(t *testing.T, s *Store, key string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM title_changes WHERE title_key = ?`, key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func probedCopy(item, source, id string, height int, audio ...string) IndexedCopy {
	c := IndexedCopy{CopyID: id, ItemID: item, SourceID: source, Fingerprint: "1-" + strings.Repeat("a", 32), ETag: "e" + id,
		Size: 100, Modified: 10, Streams: map[string]int{"v": 0, "a1": 1}, Parts: 1, ProbeVersion: 1, ProbedAt: time.Unix(1000, 0),
		Languages: &CopyLanguages{AudioLanguages: audio, Audio: audio, Subtitles: []string{"en"}},
		Facts: wire.Copy{MediaType: "video/matroska", Size: 100, DurationMs: 5000,
			Video: &wire.VideoStream{Codec: "h264", Width: height * 16 / 9, Height: height}, AudioTracks: []wire.CopyAudio{{ID: "a1", Codec: "aac"}}}}
	return c
}

// The copy index: a copy needs its item; rows come, change as served,
// and go, each a change of the title (its file's episodes too); the item's
// languages and picture follow its probed copies, and a catalog read
// doesn't put Jellyfin's back over them; a deleted item takes its copies.
func TestCopyIndex(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	n := func(i int) *int { return &i }
	film := CatalogItem{ID: id(0), Kind: "movie", Name: "F", SortName: "f", TMDB: "7", AudioTags: []string{"de"}, AudioLanguages: []string{"de"},
		Height: 480}
	series := CatalogItem{ID: id(1), Kind: "series", Name: "A", SortName: "a", TMDB: "1"}
	season := CatalogItem{ID: id(2), Kind: "season", Name: "S1", SortName: "s1", SeriesID: id(1), SeasonNumber: n(1)}
	file := CatalogItem{ID: id(3), Kind: "episode", Name: "E1", SortName: "e1", SeriesID: id(1), SeasonID: id(2), SeasonNumber: n(1),
		EpisodeNumber: n(1), EpisodeNumberEnd: n(2)}
	if err := s.PutCatalog(ctx, []CatalogItem{film, series, season, file}, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshTitleKeys(ctx); err != nil {
		t.Fatal(err)
	}

	if err := s.PutCopy(ctx, probedCopy(id(9), id(9), "vnone", 1080)); !errors.Is(err, ErrNoItem) {
		t.Errorf("a copy without its item: %v", err)
	}

	before := changes(t, s, "movie/tmdb/7")
	c := probedCopy(id(0), id(0), "vfilm", 2160, "fr-CA", "en")
	if err := s.PutCopy(ctx, c); err != nil {
		t.Fatal(err)
	}
	if changes(t, s, "movie/tmdb/7") == before {
		t.Error("a new copy isn't a change of its title")
	}
	got, ok, err := s.CopyOf(ctx, id(0), id(0))
	if err != nil || !ok || got.CopyID != "vfilm" || got.Facts.Video.Height != 2160 || got.Streams["a1"] != 1 ||
		got.Facts.ID != "" || got.Facts.ETag != "" || got.Languages == nil || !got.ProbedAt.Equal(time.Unix(1000, 0)) {
		t.Fatalf("kept: %+v %v %v", got, ok, err)
	}
	var tags, langs, picture string
	read := func() {
		t.Helper()
		s.db.QueryRow(`SELECT audio_tags, audio_languages, coalesce(picture, '') FROM catalog_items WHERE id = ?`, id(0)).
			Scan(&tags, &langs, &picture)
	}
	read()
	if tags != `["fr-CA","en"]` || langs != `["fr-CA","en"]` || picture != `{"height":2160}` {
		t.Errorf("the film's languages and picture from its copy: %s %s %s", tags, langs, picture)
	}
	// Read again from Jellyfin: its copy's stay.
	if err := s.PutCatalog(ctx, []CatalogItem{film}, 1); err != nil {
		t.Fatal(err)
	}
	read()
	if tags != `["fr-CA","en"]` || picture != `{"height":2160}` {
		t.Errorf("Jellyfin's put back: %s %s", tags, picture)
	}

	// The same copy again: nothing changes; its previews or its segments
	// are a change.
	before = changes(t, s, "movie/tmdb/7")
	if err := s.PutCopy(ctx, c); err != nil {
		t.Fatal(err)
	}
	if changes(t, s, "movie/tmdb/7") != before {
		t.Error("the same copy again told as a change")
	}
	layout := &wire.PreviewLayout{TileWidth: 320, TileHeight: 180, Columns: 10, Rows: 10, IntervalMs: 10_000, TileCount: 50}
	if err := s.SetCopyLayout(ctx, id(0), id(0), layout); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCopyLayout(ctx, id(0), id(0), layout); err != nil {
		t.Fatal(err)
	}
	if changes(t, s, "movie/tmdb/7") != before+1 {
		t.Errorf("previews: %d changes, want 1", changes(t, s, "movie/tmdb/7")-before)
	}
	film.SegmentsRead = true
	film.Segments = map[string][]wire.CopySegment{id(0): {{Kind: "outro", StartMs: 4000, EndMs: 5000}}}
	if err := s.PutCatalog(ctx, []CatalogItem{film}, 1); err != nil {
		t.Fatal(err)
	}
	if changes(t, s, "movie/tmdb/7") != before+2 {
		t.Error("segments not a change")
	}
	if segs, err := s.CopySegments(ctx, id(0), id(0)); err != nil || len(segs) != 1 || segs[0].Kind != "outro" {
		t.Errorf("segments: %v %v", segs, err)
	}
	film.SegmentsRead = false // not sent: kept
	if err := s.PutCatalog(ctx, []CatalogItem{film}, 1); err != nil {
		t.Fatal(err)
	}
	if segs, _ := s.CopySegments(ctx, id(0), id(0)); len(segs) != 1 {
		t.Error("segments forgotten when not sent")
	}

	// Renamed: another ID for the version, the old row gone.
	c.CopyID = "vrenamed"
	if err := s.PutCopy(ctx, c); err != nil {
		t.Fatal(err)
	}
	if cs, err := s.ItemCopies(ctx, id(0)); err != nil || len(cs) != 1 || cs[0].CopyID != "vrenamed" {
		t.Errorf("renamed: %+v %v", cs, err)
	}

	// A file of two episodes: its copy is a change of both.
	e2 := "series/tmdb/1/s1/e2"
	before = changes(t, s, e2)
	if err := s.PutCopy(ctx, probedCopy(id(3), id(3), "vep", 720, "ja")); err != nil {
		t.Fatal(err)
	}
	if changes(t, s, e2) == before {
		t.Error("the file's second episode not told")
	}
	if cs, _ := s.ItemCopies(ctx, rangeID(id(3), 2)); len(cs) != 1 || cs[0].CopyID != "vep" {
		t.Errorf("the second episode's copies: %+v", cs)
	}
	var second string
	s.db.QueryRow(`SELECT audio_tags FROM catalog_items WHERE id = ?`, rangeID(id(3), 2)).Scan(&second)
	if second != `["ja"]` {
		t.Errorf("the second episode's languages: %s", second)
	}

	// A minimal copy doesn't count for the languages.
	alt := IndexedCopy{CopyID: "valt", ItemID: id(0), SourceID: id(5), Position: 1, Fingerprint: "2-" + strings.Repeat("b", 32),
		ETag: "ealt", Minimal: true, Streams: map[string]int{}, Facts: wire.Copy{MediaType: "video/mp4", AudioTracks: []wire.CopyAudio{}},
		Languages: &CopyLanguages{Audio: []string{"it"}}}
	if err := s.PutCopy(ctx, alt); err != nil {
		t.Fatal(err)
	}
	read()
	if tags != `["fr-CA","en"]` {
		t.Errorf("a minimal copy's languages: %s", tags)
	}
	if got, _, _ := s.CopyOf(ctx, id(0), id(5)); got.Languages != nil {
		t.Error("a minimal copy kept languages")
	}

	// Gone from the libraries: forgotten, a change.
	before = changes(t, s, "movie/tmdb/7")
	if err := s.ForgetIndexedCopies(ctx, map[[2]string]bool{{id(0), id(0)}: true, {id(3), id(3)}: true}); err != nil {
		t.Fatal(err)
	}
	if cs, _ := s.ItemCopies(ctx, id(0)); len(cs) != 1 {
		t.Errorf("after forgetting: %d", len(cs))
	}
	if changes(t, s, "movie/tmdb/7") != before+1 {
		t.Error("a copy gone isn't a change")
	}
	// The item deleted: its copies and segments go with it.
	if err := s.DeleteCatalog(ctx, []string{id(0)}); err != nil {
		t.Fatal(err)
	}
	var left int
	s.db.QueryRow(`SELECT (SELECT count(*) FROM catalog_copies WHERE item_id = ?) + (SELECT count(*) FROM copy_segments WHERE item_id = ?)`,
		id(0), id(0)).Scan(&left)
	if left != 0 {
		t.Errorf("%d rows left of a deleted item", left)
	}
}

// Probes are kept gzipped by fingerprint, read back whole; those of files
// no copy has go; failures are remembered.
func TestCopyProbes(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	raw := bytes.Repeat([]byte(`{"streams":[]}`), 1000)
	p := CopyProbe{Fingerprint: "1-" + strings.Repeat("c", 32), ProbeVersion: 3, MediaType: "video/mp4", Size: 5, Raw: raw}
	now := time.Now()
	if err := s.CopyProbeFailed(ctx, p.Fingerprint, now); err != nil {
		t.Fatal(err)
	}
	if failed, _ := s.CopyProbesFailedSince(ctx, now.Add(-time.Minute)); !failed[p.Fingerprint] {
		t.Error("failure not noted")
	}
	if err := s.PutCopyProbe(ctx, p, now); err != nil {
		t.Fatal(err)
	}
	if failed, _ := s.CopyProbesFailedSince(ctx, now.Add(-time.Minute)); failed[p.Fingerprint] {
		t.Error("failure kept after a probe")
	}
	var stored int
	s.db.QueryRow(`SELECT length(raw) FROM copy_probes`).Scan(&stored)
	if stored >= len(raw)/4 {
		t.Errorf("not packed: %d of %d", stored, len(raw))
	}
	got, ok, err := s.CopyProbeOf(ctx, p.Fingerprint)
	if err != nil || !ok || !bytes.Equal(got.Raw, raw) || got.ProbeVersion != 3 || got.MediaType != "video/mp4" || got.Size != 5 {
		t.Fatalf("read back: %v %v", ok, err)
	}
	if err := s.ForgetCopyProbes(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.CopyProbeOf(ctx, p.Fingerprint); ok {
		t.Error("a probe no copy has kept")
	}
}

// Migration 135 makes the copy index's tables and triggers.
func TestCopiesMigration(t *testing.T) {
	s := openTest(t)
	var names []string
	rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE name LIKE '%cop%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		names = append(names, n)
	}
	rows.Close()
	for _, want := range []string{"catalog_copies", "catalog_copies_fingerprint", "copy_probe_failed", "copy_probes", "copy_segments",
		"title_changes_copy_delete", "title_changes_copy_insert", "title_changes_copy_update"} {
		if !slices.Contains(names, want) {
			t.Errorf("%s missing: %v", want, names)
		}
	}
}
