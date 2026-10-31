package store

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// The upgrade keeps Jellyfin's matches as Overfin's: nothing is matched
// again but what had none.
func TestMatchesUpgrade(t *testing.T) {
	s := openAt(t, 71)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	// As a database of then holds them (PutCatalog writes what came after).
	for _, row := range [][]any{{id(0), "Heat", "949", "Heat (1995)"}, {id(1), "Unknown", nil, "Unknown (2001)"}} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO catalog_items (id, kind, name, sort_name, seen, tmdb, jellyfin_tmdb, folder_name, rating)
			VALUES (?, 'movie', ?, ?, 1, ?, ?, ?, '')`, row[0], row[1], row[1], row[2], row[2], row[3]); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	todo, err := s.ToMatch(ctx, 10)
	if err != nil || len(todo) != 1 || todo[0].ItemID != id(1) || todo[0].FolderName != "Unknown (2001)" {
		t.Fatalf("to match: %+v %v", todo, err)
	}
	if m, ok, _ := s.MatchOf(ctx, id(0)); !ok || m.TMDB != "949" || !m.Sure {
		t.Errorf("kept: %+v %v", m, ok)
	}
}

// A match gives the item its title, after an admin's choice and before
// Jellyfin's, holds when Jellyfin is read again, moves what profiles did
// with it, and is made again only when the item's names change.
func TestSetMatch(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, prof, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	one := 1
	items := []CatalogItem{
		{ID: id(0), Kind: "series", Name: "19-2", SortName: "a", TMDB: "61546", FolderName: "19-2"},
		{ID: id(1), Kind: "episode", Name: "E1", SortName: "e", SeriesID: id(0), SeasonNumber: &one, EpisodeNumber: &one},
		{ID: id(2), Kind: "movie", Name: "Heat", SortName: "h", FolderName: "Heat (1995)", FileName: "Heat.mkv"},
	}
	s.PutCatalog(ctx, items, 1)
	s.RefreshTitleKeys(ctx)
	s.SetPlayed(ctx, prof.ID, map[string]string{"series/tmdb/61546/s1/e1": id(1)}, true, time.Now())

	todo, _ := s.ToMatch(ctx, 10)
	if len(todo) != 2 || todo[0].Episodes[1] != 1 {
		t.Fatalf("to match: %+v", todo)
	}
	m := Match{TMDB: "38527", Reasons: []MatchReason{{"several", "Several fit."}},
		Candidates: []MatchCandidate{{TMDB: "38527", Name: "19-2", Year: 2011, Score: 70}, {TMDB: "61546", Name: "19-2", Year: 2014, Score: 60}}}
	if err := s.SetMatch(ctx, id(0), m, "19-2", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	keys, _ := s.TitleKeys(ctx, []string{id(0), id(1)})
	if keys[id(0)] != "series/tmdb/38527" || keys[id(1)] != "series/tmdb/38527/s1/e1" {
		t.Errorf("keys: %v", keys)
	}
	if st, _ := s.PersonalStates(ctx, prof.ID, []string{"series/tmdb/38527/s1/e1"}); !st["series/tmdb/38527/s1/e1"].Played {
		t.Errorf("the mark didn't move: %v", st)
	}
	// Jellyfin read again (still saying 61546): the match holds.
	s.PutCatalog(ctx, items, 2)
	s.RefreshTitleKeys(ctx)
	if keys, _ := s.TitleKeys(ctx, []string{id(0)}); keys[id(0)] != "series/tmdb/38527" {
		t.Errorf("after Jellyfin: %v", keys)
	}
	got, ok, err := s.MatchOf(ctx, id(0))
	if err != nil || !ok || got.Sure || len(got.Candidates) != 2 || got.Reasons[0].Code != "several" {
		t.Errorf("kept: %+v %v %v", got, ok, err)
	}
	// An admin's choice comes first.
	s.SetOverride(ctx, id(0), "61546", time.Now())
	s.PutCatalog(ctx, items, 3)
	s.RefreshTitleKeys(ctx)
	if keys, _ := s.TitleKeys(ctx, []string{id(0)}); keys[id(0)] != "series/tmdb/61546" {
		t.Errorf("override: %v", keys)
	}
	// Matched (nothing found), the film isn't again until its names
	// change; overridden, the series isn't at all.
	s.SetMatch(ctx, id(2), Match{Reasons: []MatchReason{{"no-tmdb", "Nothing."}}}, "Heat (1995)", "Heat.mkv", time.Now())
	if todo, _ := s.ToMatch(ctx, 10); len(todo) != 0 {
		t.Errorf("still to match: %+v", todo)
	}
	items[2].FileName = "Heat (1995).mkv"
	s.PutCatalog(ctx, items, 4)
	if todo, _ := s.ToMatch(ctx, 10); len(todo) != 1 || todo[0].ItemID != id(2) {
		t.Errorf("renamed: %+v", todo)
	}
}

// What a match is told of the files' audio: a film's own languages, a
// series' episodes' together.
func TestToMatchAudio(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	one, two := 1, 2
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "movie", Name: "Film", SortName: "a", FolderName: "Film", AudioLanguages: []string{"fr", "en"}},
		{ID: id(1), Kind: "series", Name: "19-2", SortName: "b", FolderName: "19-2"},
		{ID: id(2), Kind: "episode", Name: "E1", SortName: "c", SeriesID: id(1), SeasonNumber: &one, EpisodeNumber: &one, AudioLanguages: []string{"fr"}},
		{ID: id(3), Kind: "episode", Name: "E2", SortName: "d", SeriesID: id(1), SeasonNumber: &one, EpisodeNumber: &two, AudioLanguages: []string{"fr", "en"}},
	}, 1)
	todo, err := s.ToMatch(ctx, 10)
	if err != nil || len(todo) != 2 {
		t.Fatalf("%+v %v", todo, err)
	}
	if !slices.Equal(todo[0].Audio, []string{"fr", "en"}) || !slices.Equal(todo[1].Audio, []string{"en", "fr"}) {
		t.Errorf("film %v, series %v", todo[0].Audio, todo[1].Audio)
	}
}
