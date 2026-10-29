package catalog

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// slowReleases is TMDB finding ten films, each release taking a moment.
type slowReleases struct {
	fakeTitles
	mu            sync.Mutex
	running, most int
}

func (f *slowReleases) Search(context.Context, string) ([]tmdb.Title, []tmdb.PersonHit, error) {
	var out []tmdb.Title
	for i := 1; i <= 12; i++ {
		out = append(out, tmdb.Title{TMDBID: i, Kind: "movie", Name: "Film", Year: 2000})
	}
	return out, nil, nil
}

func (f *slowReleases) Release(_ context.Context, _ string, id int) (*tmdb.Release, error) {
	f.mu.Lock()
	f.running++
	f.most = max(f.most, f.running)
	f.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	f.mu.Lock()
	f.running--
	f.mu.Unlock()
	return &tmdb.Release{Released: "2000-01-01", AtHome: "2000-06-01"}, nil
}

// A search asks TMDB the first ten titles' releases a few at a time, not
// one after another, and keeps them all.
func TestSearchAsksReleasesTogether(t *testing.T) {
	ctx := context.Background()
	box, _ := seal.New(bytes.Repeat([]byte{9}, 32))
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	src := &slowReleases{}
	titles := NewTitles(func() TitleSource { return src }, st, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour, 0)
	start := time.Now()
	found, _, _, err := titles.Search(ctx, "film")
	if err != nil || len(found) != 12 {
		t.Fatalf("search: %d %v", len(found), err)
	}
	if took := time.Since(start); took > 300*time.Millisecond {
		t.Errorf("took %v: one after another", took)
	}
	if src.most < 2 || src.most > searchReleases {
		t.Errorf("at most %d at once", src.most)
	}
	for i := 1; i <= 10; i++ {
		if _, _, known, _ := st.TitleRelease(ctx, "movie", strconv.Itoa(i)); !known {
			t.Errorf("film %d's release not kept", i)
		}
	}
}
