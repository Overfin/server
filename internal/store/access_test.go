package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// A film in two libraries (4K and HD) is one title in listings: once on
// the pages, once in the count.
func TestListingOneCopyPerTitle(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "movie", Name: "Arrival", SortName: "a", TMDB: "1", LibraryID: "films"},
		{ID: id(1), Kind: "movie", Name: "Arrival", SortName: "a", TMDB: "1", LibraryID: "films-4k"},
		{ID: id(2), Kind: "movie", Name: "Heat", SortName: "h", TMDB: "2", LibraryID: "films"},
	}, 1)
	if err := s.RefreshTitleKeys(ctx); err != nil {
		t.Fatal(err)
	}
	var all []string
	for offset := 0; offset < 4; offset++ {
		page, err := s.ListCatalog(ctx, ListQuery{Kinds: []string{"movie"}, Sort: "name", Offset: offset, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page...)
	}
	if !slices.Equal(all, []string{id(0), id(2)}) {
		t.Errorf("pages: %v", all)
	}
	if n, err := s.CountCatalog(ctx, ListQuery{Kinds: []string{"movie"}}); err != nil || n != 2 {
		t.Errorf("count: %d %v", n, err)
	}
	// Within one library, its own copy.
	if got, _ := s.ListCatalog(ctx, ListQuery{LibraryID: "films-4k", Sort: "name", Limit: 10}); !slices.Equal(got, []string{id(1)}) {
		t.Errorf("the 4K library: %v", got)
	}
}

// A hidden tag hides it whatever the case of any letter, accented ones
// too, as the tags listed and the profile's settings count them.
func TestHiddenTagAnyCase(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "movie", Name: "Peur", SortName: "a", TMDB: "1", LibraryID: "films", Tags: []string{"épouvante"}},
		{ID: id(1), Kind: "movie", Name: "Effroi", SortName: "b", TMDB: "2", LibraryID: "films", Tags: []string{"Épouvante"}},
		{ID: id(2), Kind: "movie", Name: "Rire", SortName: "c", TMDB: "3", LibraryID: "films"},
	}, 1)
	if err := s.RefreshTitleKeys(ctx); err != nil {
		t.Fatal(err)
	}
	a := Access{BlockedTags: []string{"ÉPOUVANTE"}}
	got, err := s.ListCatalog(ctx, ListQuery{Access: &a, Sort: "name", Limit: 50})
	if err != nil || !slices.Equal(got, []string{id(2)}) {
		t.Errorf("listed: %v %v", got, err)
	}
	allowed, _, err := s.AllowedItems(ctx, a, []string{id(0), id(1), id(2)})
	if err != nil || allowed[id(0)] || allowed[id(1)] || !allowed[id(2)] {
		t.Errorf("allowed: %v %v", allowed, err)
	}
	if tags, _ := s.CatalogTags(ctx); len(tags) != 1 {
		t.Errorf("tags: %v", tags)
	}
	if !a.Blocks([]string{"épouvante"}) {
		t.Error("Blocks disagrees")
	}
}

func TestAccessAndListings(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	one, two := 1, 2
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "movie", Name: "Heat", SortName: "a", TMDB: "1", LibraryID: "films", People: []string{"p1"}},
		{ID: id(1), Kind: "movie", Name: "Gore", SortName: "b", TMDB: "2", LibraryID: "films", Tags: []string{"Horror"}},
		{ID: id(2), Kind: "series", Name: "Show", SortName: "c", TMDB: "3", LibraryID: "tv", Tags: []string{"horror"}, People: []string{"p1"}},
		{ID: id(3), Kind: "episode", Name: "E1", SortName: "d", SeriesID: id(2), SeasonNumber: &one, EpisodeNumber: &one, LibraryID: "tv"},
		{ID: id(4), Kind: "episode", Name: "E2", SortName: "e", SeriesID: id(2), SeasonNumber: &one, EpisodeNumber: &two, LibraryID: "tv"},
		{ID: id(5), Kind: "series", Name: "Other", SortName: "f", TMDB: "4", LibraryID: "tv"},
	}, 1)
	if err := s.RefreshTitleKeys(ctx); err != nil {
		t.Fatal(err)
	}
	list := func(q ListQuery) []string {
		t.Helper()
		if q.Sort == "" {
			q.Sort = "name"
		}
		q.Limit = 50
		got, err := s.ListCatalog(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	// Libraries: only those the profile gets; none at all, nothing.
	if got := list(ListQuery{Access: &Access{Libraries: []string{"films"}}}); !slices.Equal(got, []string{id(0), id(1)}) {
		t.Errorf("films only: %v", got)
	}
	if got := list(ListQuery{Access: &Access{Libraries: []string{}}}); len(got) != 0 {
		t.Errorf("no library: %v", got)
	}
	// A tag hides the title, and a series' tag its episodes, whatever the case.
	if got := list(ListQuery{Access: &Access{BlockedTags: []string{"HORROR"}}}); !slices.Equal(got, []string{id(0), id(5)}) {
		t.Errorf("horror hidden: %v", got)
	}
	tags, _ := s.CatalogTags(ctx)
	if len(tags) != 1 {
		t.Errorf("tags: %v", tags)
	}

	acct, prof, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	_ = acct
	keys, _ := s.TitleKeys(ctx, []string{id(0), id(3), id(4)})
	at := time.Unix(1_000_000, 0)
	// A series is watched once all its episodes are; in progress while one is.
	played := true
	s.SetPlayed(ctx, prof.ID, map[string]string{keys[id(3)]: id(3)}, true, at)
	if got := list(ListQuery{Kinds: []string{"series"}, ProfileID: prof.ID, Played: &played}); len(got) != 0 {
		t.Errorf("half watched as watched: %v", got)
	}
	s.RecordPlay(ctx, prof.ID, keys[id(4)], id(4), 60_000, false, at.Add(time.Hour))
	if got := list(ListQuery{Kinds: []string{"series"}, ProfileID: prof.ID, InProgress: true}); !slices.Equal(got, []string{id(2)}) {
		t.Errorf("in progress: %v", got)
	}
	s.SetPlayed(ctx, prof.ID, map[string]string{keys[id(4)]: id(4)}, true, at.Add(2*time.Hour))
	if got := list(ListQuery{Kinds: []string{"series"}, ProfileID: prof.ID, Played: &played}); !slices.Equal(got, []string{id(2)}) {
		t.Errorf("watched: %v", got)
	}
	unplayed := false
	if got := list(ListQuery{Kinds: []string{"series"}, ProfileID: prof.ID, Played: &unplayed}); !slices.Equal(got, []string{id(5)}) {
		t.Errorf("unwatched: %v", got)
	}
	// Last played first: the series (through its episodes), then the film;
	// what was never played comes last.
	s.SetPlayed(ctx, prof.ID, map[string]string{keys[id(0)]: id(0)}, true, at.Add(-time.Hour))
	got := list(ListQuery{Kinds: []string{"movie", "series"}, ProfileID: prof.ID, Sort: "lastPlayed", Descending: true})
	if len(got) != 4 || got[0] != id(2) || got[1] != id(0) {
		t.Errorf("last played: %v", got)
	}
	// Everything, for shuffling.
	if got := list(ListQuery{Kinds: []string{"movie"}, Limit: -1}); len(got) != 2 {
		t.Errorf("all: %v", got)
	}

	// A profile's access, kept and read back.
	if err := s.SetProfileAccess(ctx, acct.ID, prof.ID, []string{"films"}, []string{"Horror"}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Profile(ctx, acct.ID, prof.ID)
	if !slices.Equal(p.Libraries, []string{"films"}) || !slices.Equal(p.BlockedTags, []string{"Horror"}) {
		t.Errorf("access: %+v", p)
	}
	if a := AccessOf(p); a == nil || !a.GetsLibrary("films") || a.GetsLibrary("tv") || !a.Blocks([]string{"horror"}) {
		t.Errorf("access of: %+v", a)
	}
	s.SetProfileAccess(ctx, acct.ID, prof.ID, nil, nil)
	if p, _ := s.Profile(ctx, acct.ID, prof.ID); p.Libraries != nil || AccessOf(p) != nil {
		t.Errorf("cleared: %+v", p)
	}
}

// The watched, not watched and in progress filters, for seasons as for
// series, and for an episode in two copies (watched through either).
func TestPlayedFilters(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	one, two := 1, 2
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "series", Name: "Show", SortName: "a", TMDB: "1", LibraryID: "tv"},
		{ID: id(1), Kind: "season", Name: "S1", SortName: "b", SeriesID: id(0), SeasonNumber: &one, LibraryID: "tv"},
		{ID: id(2), Kind: "season", Name: "S2", SortName: "c", SeriesID: id(0), SeasonNumber: &two, LibraryID: "tv"},
		{ID: id(3), Kind: "episode", Name: "E1", SortName: "d", SeriesID: id(0), SeasonID: id(1), SeasonNumber: &one, EpisodeNumber: &one, LibraryID: "tv"},
		// The same episode again (another version).
		{ID: id(4), Kind: "episode", Name: "E1", SortName: "e", SeriesID: id(0), SeasonID: id(1), SeasonNumber: &one, EpisodeNumber: &one, LibraryID: "tv"},
		{ID: id(5), Kind: "episode", Name: "E1", SortName: "f", SeriesID: id(0), SeasonID: id(2), SeasonNumber: &two, EpisodeNumber: &one, LibraryID: "tv"},
		// A season with no episode yet.
		{ID: id(6), Kind: "season", Name: "S3", SortName: "g", SeriesID: id(0), SeasonNumber: new(int), LibraryID: "tv"},
		{ID: id(7), Kind: "movie", Name: "Film", SortName: "h", TMDB: "2", LibraryID: "films"},
	}, 1)
	if err := s.RefreshTitleKeys(ctx); err != nil {
		t.Fatal(err)
	}
	_, prof, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	keys, _ := s.TitleKeys(ctx, []string{id(4), id(5), id(7)})
	at := time.Unix(1_000_000, 0)
	yes, no := true, false
	list := func(q ListQuery) []string {
		t.Helper()
		q.ProfileID, q.Sort, q.Limit = prof.ID, "name", 50
		got, err := s.ListCatalog(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	kinds := []string{"series", "season", "movie"}
	for _, step := range []struct {
		do                         func()
		watched, unwatched, inProg []string
	}{
		{func() {}, nil, []string{id(0), id(1), id(2), id(6), id(7)}, nil},
		// Season 1's only episode, watched through its second copy.
		{func() { s.SetPlayed(ctx, prof.ID, map[string]string{keys[id(4)]: id(4)}, true, at) },
			[]string{id(1)}, []string{id(0), id(2), id(6), id(7)}, nil},
		{func() { s.RecordPlay(ctx, prof.ID, keys[id(5)], id(5), 60_000, false, at) },
			[]string{id(1)}, []string{id(0), id(2), id(6), id(7)}, []string{id(0), id(2)}},
		{func() {
			s.SetPlayed(ctx, prof.ID, map[string]string{keys[id(5)]: id(5), keys[id(7)]: id(7)}, true, at)
		}, []string{id(0), id(1), id(2), id(7)}, []string{id(6)}, nil},
	} {
		step.do()
		if got := list(ListQuery{Kinds: kinds, Played: &yes}); !slices.Equal(got, step.watched) {
			t.Errorf("watched: %v, want %v", got, step.watched)
		}
		if got := list(ListQuery{Kinds: kinds, Played: &no}); !slices.Equal(got, step.unwatched) {
			t.Errorf("not watched: %v, want %v", got, step.unwatched)
		}
		if got := list(ListQuery{Kinds: kinds, InProgress: true}); !slices.Equal(got, step.inProg) {
			t.Errorf("in progress: %v, want %v", got, step.inProg)
		}
	}
}

// A series' watched and in-progress filters, which look its episodes up
// in the profile's history by the series' key: a series named like
// another's start, one the index can't name (its episodes keyed by their
// own IDs), an episode without numbers, two copies, and history for
// episodes the libraries don't have.
func TestSeriesPlayedFilters(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 30) + fmt.Sprintf("%02d", n) }
	one, two := 1, 2
	ep := func(n int, series string, season, number *int) CatalogItem {
		return CatalogItem{ID: id(n), Kind: "episode", Name: "E", SortName: "e", SeriesID: series, SeasonNumber: season,
			EpisodeNumber: number, LibraryID: "tv"}
	}
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "series", Name: "A", SortName: "a", TMDB: "1", LibraryID: "tv"},
		ep(1, id(0), &one, &one), ep(2, id(0), &one, &two),
		{ID: id(3), Kind: "series", Name: "B", SortName: "b", TMDB: "13", LibraryID: "tv"},
		ep(4, id(3), &one, &one),
		// No ID: its episode's key is its own.
		{ID: id(5), Kind: "series", Name: "C", SortName: "c", LibraryID: "tv"},
		ep(6, id(5), &one, &one),
		// An episode without its number.
		{ID: id(7), Kind: "series", Name: "D", SortName: "d", TMDB: "4", LibraryID: "tv"},
		ep(8, id(7), &one, nil),
		// Two copies.
		{ID: id(9), Kind: "series", Name: "E", SortName: "e1", TMDB: "5", LibraryID: "tv"},
		ep(10, id(9), &one, &one),
		{ID: id(11), Kind: "series", Name: "E", SortName: "e2", TMDB: "5", LibraryID: "tv4k"},
		ep(12, id(11), &one, &one),
		// No episode.
		{ID: id(13), Kind: "series", Name: "F", SortName: "f", TMDB: "6", LibraryID: "tv"},
		// Two copies, one without the episode.
		{ID: id(14), Kind: "series", Name: "G", SortName: "g1", TMDB: "7", LibraryID: "tv"},
		ep(15, id(14), &one, &one),
		{ID: id(16), Kind: "series", Name: "G", SortName: "g2", TMDB: "7", LibraryID: "tv4k"},
	}, 1)
	if err := s.RefreshTitleKeys(ctx); err != nil {
		t.Fatal(err)
	}
	_, prof, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	keys, _ := s.TitleKeys(ctx, []string{id(1), id(2), id(4), id(6), id(8), id(10), id(15)})
	at := time.Unix(1_000_000, 0)
	s.SetPlayed(ctx, prof.ID, map[string]string{keys[id(1)]: id(1), keys[id(2)]: id(2), keys[id(6)]: id(6),
		keys[id(8)]: id(8), keys[id(10)]: id(10), "series/tmdb/6/s1/e1": ""}, true, at)
	s.RecordPlay(ctx, prof.ID, keys[id(4)], id(4), 60_000, false, at)
	s.RecordPlay(ctx, prof.ID, keys[id(15)], id(15), 60_000, false, at)
	s.RecordPlay(ctx, prof.ID, "series/tmdb/6/s1/e2", "", 60_000, false, at)
	if !strings.HasPrefix(keys[id(6)], "item/") || !strings.HasPrefix(keys[id(8)], "item/") {
		t.Fatalf("keys: %v", keys)
	}
	yes, no := true, false
	for _, c := range []struct {
		q    ListQuery
		want []string
	}{
		{ListQuery{Played: &yes}, []string{id(0), id(5), id(7), id(9), id(11)}},
		{ListQuery{Played: &no}, []string{id(3), id(13), id(14), id(16)}},
		{ListQuery{InProgress: true}, []string{id(3), id(14)}},
	} {
		// Every copy (a listing shows one).
		c.q.Kinds, c.q.ProfileID = []string{"series"}, prof.ID
		got, err := s.CatalogIDs(ctx, c.q)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(got)
		if !slices.Equal(got, c.want) {
			t.Errorf("%+v: %v, want %v", c.q, got, c.want)
		}
	}
}
