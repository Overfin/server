package catalog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// failingTitles answers every title with err, and counts what it's asked.
type failingTitles struct {
	fakeTitles
	err   error
	asked int
}

func (f *failingTitles) Title(context.Context, string, int) (*tmdb.Full, error) {
	f.asked++
	return nil, f.err
}

// A pass asks TMDB as little as it can when TMDB fails: once a token is
// refused (401), nothing more; after ten failures in a row, the pass ends
// and the rest waits for the next.
func TestTitlesStopAskingOnFailures(t *testing.T) {
	for _, c := range []struct {
		name  string
		err   error
		asked int
	}{
		{"a refused token", tmdb.ErrUnauthorized, 1},
		{"TMDB failing", errors.New("tmdb: 503"), 10},
	} {
		box, _ := seal.New(bytes.Repeat([]byte{9}, 32))
		st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		var items []store.CatalogItem
		for i := range 25 {
			items = append(items, store.CatalogItem{ID: fmt.Sprintf("%032x", i+1), Kind: "movie", Name: "Film " + strconv.Itoa(i),
				SortName: fmt.Sprintf("f%02d", i), TMDB: strconv.Itoa(100 + i)})
		}
		st.PutCatalog(ctx, items, 1)
		src := &failingTitles{err: c.err}
		k := NewTitles(func() TitleSource { return src }, st, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour, 0)
		if err := k.Once(ctx); err == nil || !errors.Is(err, c.err) {
			t.Errorf("%s: the pass ended with %v", c.name, err)
		}
		if src.asked != c.asked {
			t.Errorf("%s: TMDB asked %d times, want %d", c.name, src.asked, c.asked)
		}
		st.Close()
	}
}
