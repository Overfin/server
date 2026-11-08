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

	"github.com/overfin/server/internal/imdb"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// listSource is TMDB with one title in each of its lists, finding titles
// by IMDb ID too, counting what it's asked.
type listSource struct {
	discovered map[string]int // by list
	found      map[string]int // by IMDb ID
}

func (s *listSource) Discover(ctx context.Context, list, kind string, page int) (*tmdb.Page, error) {
	s.discovered[list]++
	return &tmdb.Page{Page: 1, TotalPages: 1, Titles: []tmdb.Title{{Kind: kind, TMDBID: 900, Name: "TMDB's pick"}}}, nil
}

func (s *listSource) FindTitle(ctx context.Context, kind, imdbID string) (tmdb.Title, error) {
	s.found[imdbID]++
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
