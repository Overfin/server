package catalog

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/overfin/server/internal/imdb"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// listSource is TMDB with one title in each of its lists, finding titles
// by IMDb ID too, counting what it's asked.
type listSource struct {
	discovered map[string]int  // by list
	found      map[string]int  // by IMDb ID
	failing    map[string]bool // IMDb IDs TMDB fails on; set, tt0000003 is film 503
}

func (s *listSource) Discover(ctx context.Context, list, kind string, page int) (*tmdb.Page, error) {
	s.discovered[list]++
	return &tmdb.Page{Page: 1, TotalPages: 1, Titles: []tmdb.Title{{Kind: kind, TMDBID: 900, Name: "TMDB's pick"}}}, nil
}

func (s *listSource) FindTitle(ctx context.Context, kind, imdbID string) (tmdb.Title, error) {
	s.found[imdbID]++
	if s.failing[imdbID] {
		return tmdb.Title{}, errors.New("TMDB failed")
	}
	if s.failing != nil && imdbID == "tt0000003" && kind == "movie" {
		return tmdb.Title{Kind: kind, TMDBID: 503, Name: "Found"}, nil
	}
	if imdbID == "tt0000002" && kind == "movie" {
		return tmdb.Title{Kind: kind, TMDBID: 502, Name: "Found"}, nil
	}
	return tmdb.Title{}, tmdb.ErrNotFound
}

func (s *listSource) Details(context.Context, string, int) (*tmdb.Details, error) {
	return nil, tmdb.ErrNotFound
}

func (s *listSource) Certifications(context.Context, string) (map[string][]tmdb.Certification, error) {
	return nil, nil
}

func (s *listSource) Release(context.Context, string, int) (*tmdb.Release, error) {
	return nil, tmdb.ErrNotFound
}

// With IMDb's lists, trending and popular are theirs, each title found on
// TMDB once by its IMDb ID (one known already isn't asked; one TMDB knows
// none of is left out, and not asked again soon); upcoming stays TMDB's.
// Without them, TMDB's lists come back.
func TestListsFromIMDb(t *testing.T) {
	ctx := context.Background()
	box, _ := seal.New(bytes.Repeat([]byte{9}, 32))
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// tt0000001 was found before: film 501.
	st.PutTitles(ctx, []store.Title{{Kind: "movie", TMDB: "501", Name: "Known"}}, now)
	st.PutFound(ctx, "movie", "tt0000001", "501", now)

	src := &listSource{discovered: map[string]int{}, found: map[string]int{}}
	ranked := true
	l := NewLists(func() tmdb.Source { return src }, st, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour, 1)
	l.IMDb = func(list string) (imdb.Ranked, bool) {
		return imdb.Ranked{Movies: []string{"tt0000001", "tt0000002", "tt0000003"}}, ranked
	}
	listed := func(list, kind string) []string {
		titles, err := st.ListTitles(ctx, list, kind, "all", nil, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, t := range titles {
			ids = append(ids, t.TMDB)
		}
		return ids
	}
	for range 2 {
		if err := l.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, list := range []string{"trending", "popular"} {
		if got := listed(list, "movie"); !slices.Equal(got, []string{"501", "502"}) {
			t.Errorf("%s films: %v", list, got)
		}
		if got := listed(list, "series"); len(got) != 0 {
			t.Errorf("%s series: %v", list, got)
		}
		if src.discovered[list] != 0 {
			t.Errorf("TMDB's %s asked", list)
		}
	}
	if got := listed("upcoming", "movie"); !slices.Equal(got, []string{"900"}) || src.discovered["upcoming"] != 4 {
		t.Errorf("upcoming: %v, asked %d", got, src.discovered["upcoming"])
	}
	// Asked once each over both passes and both lists; the known one never.
	if src.found["tt0000001"] != 0 || src.found["tt0000002"] != 1 || src.found["tt0000003"] != 1 {
		t.Errorf("found: %v", src.found)
	}

	ranked = false
	if err := l.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := listed("trending", "movie"); !slices.Equal(got, []string{"900"}) {
		t.Errorf("without IMDb's lists: %v", got)
	}
}

// One title TMDB fails on doesn't cost the list or those after it, and is
// asked about again next time; TMDB down leaves the lists as they are, and
// so does a ranking of titles nobody knows.
func TestListsFromIMDbFailures(t *testing.T) {
	ctx := context.Background()
	box, _ := seal.New(bytes.Repeat([]byte{9}, 32))
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	src := &listSource{discovered: map[string]int{}, found: map[string]int{}, failing: map[string]bool{"tt0000002": true}}
	var ids []string
	l := NewLists(func() tmdb.Source { return src }, st, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour, 1)
	l.IMDb = func(list string) (imdb.Ranked, bool) { return imdb.Ranked{Movies: ids}, true }
	listed := func() []string {
		titles, err := st.ListTitles(ctx, "trending", "movie", "all", nil, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, t := range titles {
			out = append(out, t.TMDB)
		}
		return out
	}
	ids = []string{"tt0000002", "tt0000003"}
	if err := l.Refresh(ctx); err != nil {
		t.Fatalf("one failure stops the refresh: %v", err)
	}
	if got := listed(); !slices.Equal(got, []string{"503"}) {
		t.Errorf("after one failure: %v", got)
	}
	if got, _ := st.ListTitles(ctx, "upcoming", "movie", "all", nil, 0, 10); len(got) == 0 {
		t.Error("the lists after it weren't read")
	}
	if src.found["tt0000002"] < 2 {
		t.Errorf("the failed one isn't asked about again: %v", src.found)
	}

	// Five in a row: an error, and the list as it was.
	ids = []string{"tt0000011", "tt0000012", "tt0000013", "tt0000014", "tt0000015", "tt0000003"}
	for _, id := range ids[:5] {
		src.failing[id] = true
	}
	if err := l.Refresh(ctx); err == nil {
		t.Error("TMDB down: no error")
	}
	if got := listed(); !slices.Equal(got, []string{"503"}) {
		t.Errorf("TMDB down: %v", got)
	}

	// Nothing known or found: the list stays.
	src.failing = map[string]bool{}
	ids = []string{"tt0000000"}
	if err := l.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := listed(); !slices.Equal(got, []string{"503"}) {
		t.Errorf("nothing found: %v", got)
	}
}

// crossSource is TMDB whose lists have a title in a server language with
// few votes, two in another language (one short of CrossingVotes, one at
// it) and one whose language TMDB doesn't say.
type crossSource struct{ listSource }

func (s *crossSource) Discover(ctx context.Context, list, kind string, page int) (*tmdb.Page, error) {
	return &tmdb.Page{Page: 1, TotalPages: 1, Titles: []tmdb.Title{
		{Kind: kind, TMDBID: 1, Name: "Québécois", Language: "fr", Votes: 2},
		{Kind: kind, TMDBID: 2, Name: "Regional", Language: "hi", Votes: CrossingVotes - 1},
		{Kind: kind, TMDBID: 3, Name: "Crossover", Language: "ko", Votes: CrossingVotes},
		{Kind: kind, TMDBID: 4, Name: "Unsaid"},
	}}, nil
}

// Trending and popular keep what crossed borders and what's in the
// server's languages (fr-CA is French); upcoming keeps everything.
func TestListsCrossing(t *testing.T) {
	ctx := context.Background()
	box, _ := seal.New(bytes.Repeat([]byte{9}, 32))
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetLanguages(ctx, []string{"fr-CA", "en-CA"}, "fr-CA"); err != nil {
		t.Fatal(err)
	}
	src := &crossSource{listSource{discovered: map[string]int{}, found: map[string]int{}}}
	l := NewLists(func() tmdb.Source { return src }, st, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour, 1)
	if err := l.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	for list, want := range map[string][]string{"trending": {"1", "3", "4"}, "popular": {"1", "3", "4"}, "upcoming": {"1", "2", "3", "4"}} {
		titles, err := st.ListTitles(ctx, list, "series", "all", nil, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, t := range titles {
			got = append(got, t.TMDB)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", list, got, want)
		}
	}
}
