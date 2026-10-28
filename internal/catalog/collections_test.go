package catalog

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// fakeCollections is TMDB with one collection, 10 (films 1, 2 and 3), and
// films' pages.
type fakeCollections struct {
	fakeTitles
	pages   []int // films whose page was asked
	failing bool  // FilmCollection fails
}

func (f *fakeCollections) FilmCollection(_ context.Context, id int) (int, error) {
	switch {
	case f.failing:
		return 0, errors.New("TMDB is down")
	case id == 1 || id == 2:
		return 10, nil
	case id == 99:
		return 0, tmdb.ErrNotFound
	}
	return 0, nil
}

func (f *fakeCollections) Collection(_ context.Context, id int) (*tmdb.Collection, error) {
	if id != 10 {
		return nil, tmdb.ErrNotFound
	}
	film := func(id int, name string) tmdb.Title {
		return tmdb.Title{TMDBID: id, Kind: "movie", Name: name, Year: 1977 + id}
	}
	return &tmdb.Collection{ID: 10, Name: "Saga", Overview: "Three films.", Poster: "/sagaposter1.jpg",
		Parts: []tmdb.Title{film(1, "One"), film(2, "Two"), film(3, "Three")}}, nil
}

func (f *fakeCollections) Title(_ context.Context, kind string, id int) (*tmdb.Full, error) {
	f.pages = append(f.pages, id)
	return &tmdb.Full{Details: tmdb.Details{Title: tmdb.Title{TMDBID: id, Kind: kind, Name: "Film " + strconv.Itoa(id), Year: 2000}},
		Collection: 10, Release: &tmdb.Release{Ratings: tmdb.Ratings{}, Released: "2000-01-01", AtHome: "2000-01-01"}}, nil
}

// The films whose page was kept before get their collection, once; each
// collection a library's film belongs to is kept with its films, and the
// page of each film no library has is fetched; all of it is fetched again
// once old, and deleted once too old.
func TestCollectionsKept(t *testing.T) {
	ctx := context.Background()
	box, _ := seal.New(bytes.Repeat([]byte{9}, 32))
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	lib := "f1000000000000000000000000000000"
	st.PutCatalog(ctx, []store.CatalogItem{
		{ID: "a1000000000000000000000000000000", Kind: "movie", Name: "One", SortName: "one", TMDB: "1", LibraryID: lib},
		{ID: "a2000000000000000000000000000000", Kind: "movie", Name: "Lonely", SortName: "lonely", TMDB: "5", LibraryID: lib},
	}, 1)
	st.RefreshTitleKeys(ctx)
	// Kept before collections were: on the list to find, as migration 067
	// lists them.
	for _, id := range []string{"1", "5", "99"} {
		st.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: id, Name: "Film " + id}, []byte(`{}`), now)
		if err := st.AddFilmToFind(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	src := &fakeCollections{failing: true}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	titles := NewTitles(func() TitleSource { return src }, st, logger, time.Hour, 0)
	c := &Collections{Store: st, Source: func() CollectionSource { return src }, Titles: titles, Logger: logger}
	clock := now
	c.now = func() time.Time { return clock }
	titles.now = c.now

	// TMDB failing: everything stays on the list.
	if err := c.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if left, _ := st.FilmsToFind(ctx, 10); len(left) != 3 {
		t.Errorf("TMDB failing, left to find: %v", left)
	}

	src.failing = false
	if err := c.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if left, _ := st.FilmsToFind(ctx, 10); len(left) != 0 {
		t.Errorf("left to find: %v", left)
	}
	all, err := st.Collections(ctx)
	if err != nil || len(all) != 1 || all[0].Name != "Saga" || !slices.Equal(all[0].Parts, []string{"1", "2", "3"}) {
		t.Fatalf("collections: %+v %v", all, err)
	}
	if got, _ := st.FilmCollection(ctx, "5"); got != nil {
		t.Errorf("a film in none: %+v", got)
	}
	// The pages of the films no library has (2 and 3): 1's is kept.
	slices.Sort(src.pages)
	if !slices.Equal(src.pages, []int{2, 3}) {
		t.Errorf("pages fetched: %v", src.pages)
	}
	if got, _ := st.FilmCollection(ctx, "3"); got == nil || got.ID != 10 {
		t.Errorf("film 3's collection: %+v", got)
	}

	// Nothing new: nothing fetched.
	src.pages = nil
	c.Once(ctx)
	if len(src.pages) != 0 {
		t.Errorf("fetched again: %v", src.pages)
	}
	// Old: fetched again, the films' pages too.
	clock = now.Add(TitlesFresh + time.Hour)
	c.Once(ctx)
	slices.Sort(src.pages)
	if !slices.Equal(src.pages, []int{1, 2, 3}) {
		t.Errorf("fetched again once old: %v", src.pages)
	}
	// Too old, TMDB gone: deleted.
	clock = now.Add(TitlesFresh + Kept + 2*time.Hour)
	c.Source = func() CollectionSource { return nil }
	c.Once(ctx)
	if all, _ := st.Collections(ctx); len(all) != 0 {
		t.Errorf("kept too long: %+v", all)
	}
}
