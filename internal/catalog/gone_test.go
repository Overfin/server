package catalog

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// fakeMerged is TMDB after merging film 100 into 200 (IMDb tt0000001);
// film 300 is gone too, with nothing to find it by.
type fakeMerged struct{ finds int }

func (f *fakeMerged) Title(_ context.Context, kind string, id int) (*tmdb.Full, error) {
	if kind != "movie" || id != 200 {
		return nil, tmdb.ErrNotFound
	}
	return &tmdb.Full{Details: tmdb.Details{Title: tmdb.Title{TMDBID: 200, Kind: "movie", Name: "Maa", Year: 2026}}}, nil
}

func (f *fakeMerged) FindAll(_ context.Context, source, id string) ([]tmdb.Named, error) {
	f.finds++
	if source == "imdb" && id == "tt0000001" {
		return []tmdb.Named{{Kind: "movie", TMDB: 200}}, nil
	}
	return nil, nil
}

func (f *fakeMerged) Season(context.Context, int, int) (*tmdb.SeasonFull, error) {
	return nil, tmdb.ErrNotFound
}
func (f *fakeMerged) Seasons(context.Context, int, []int) (map[int]*tmdb.SeasonFull, error) {
	return nil, nil
}
func (f *fakeMerged) Find(context.Context, string, string, string) (int, error) {
	return 0, tmdb.ErrNotFound
}
func (f *fakeMerged) Person(context.Context, int) (*tmdb.PersonFull, error) {
	return nil, tmdb.ErrNotFound
}
func (f *fakeMerged) Release(context.Context, string, int) (*tmdb.Release, error) {
	return nil, tmdb.ErrNotFound
}
func (f *fakeMerged) Search(context.Context, string) ([]tmdb.Title, []tmdb.PersonHit, error) {
	return nil, nil, nil
}
func (f *fakeMerged) SearchKind(context.Context, string, string, int) ([]tmdb.Found, error) {
	return nil, nil
}

// A refresh meeting a title TMDB merged into another moves the library's
// copies to it, an admin's choice too, and keeps the old ID as gone; one
// with nothing to find the new ID by stays, kept as gone without one.
func TestTitlesRetireMerged(t *testing.T) {
	box, _ := seal.New(bytes.Repeat([]byte{9}, 32))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	st.PutCatalog(ctx, []store.CatalogItem{
		{ID: id(1), Kind: "movie", Name: "Maa", SortName: "maa", TMDB: "100", IMDb: "tt0000001", FolderName: "Maa (2026) [tmdbid-100]"},
		{ID: id(2), Kind: "movie", Name: "Maa", SortName: "maa2", TMDB: "100", IMDb: "tt0000001", FolderName: "Maa copy"},
		{ID: id(3), Kind: "movie", Name: "Lost", SortName: "lost", TMDB: "300", FolderName: "Lost (2020)"},
	}, 1)
	at := time.Unix(1_700_000_000, 0)
	if err := st.SetOverride(ctx, id(2), "100", at); err != nil {
		t.Fatal(err)
	}
	src := &fakeMerged{}
	k := NewTitles(func() TitleSource { return src }, st, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour, 0)
	if err := k.Once(ctx); err != nil {
		t.Fatal(err)
	}
	keys, _ := st.TitleKeys(ctx, []string{id(1), id(2), id(3)})
	if keys[id(1)] != "movie/tmdb/200" || keys[id(2)] != "movie/tmdb/200" || keys[id(3)] != "movie/tmdb/300" {
		t.Fatalf("keys: %v", keys)
	}
	if o, _ := st.Overrides(ctx); o[id(2)] != "200" {
		t.Fatalf("admin's choice: %v", o)
	}
	if now, gone, _ := st.GoneOf(ctx, "movie", "100"); !gone || now != "200" {
		t.Fatalf("100: %q %v", now, gone)
	}
	if now, gone, _ := st.GoneOf(ctx, "movie", "300"); !gone || now != "" {
		t.Fatalf("300: %q %v", now, gone)
	}
	if title, err := st.TitleByTMDB(ctx, "movie", "200"); err != nil || title == nil || title.Name != "Maa" {
		t.Fatalf("new title's page: %+v %v", title, err)
	}
	if k := st.CurrentKey(ctx, "movie/tmdb/100"); k != "movie/tmdb/200" {
		t.Fatalf("current key: %s", k)
	}
}
