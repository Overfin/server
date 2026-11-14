package catalog

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// listSource is TMDB with one title in each of its lists, counting what
// it's asked.
type listSource struct {
	discovered map[string]int // by list
}

func (s *listSource) Discover(ctx context.Context, list, kind string, page int) (*tmdb.Page, error) {
	s.discovered[list]++
	return &tmdb.Page{Page: 1, TotalPages: 1, Titles: []tmdb.Title{{Kind: kind, TMDBID: 900, Name: "TMDB's pick"}}}, nil
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
	src := &crossSource{listSource{discovered: map[string]int{}}}
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
