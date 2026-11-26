package ratings

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

func films(t *testing.T, n int) *store.Store {
	t.Helper()
	box, _ := seal.New(bytes.Repeat([]byte{1}, 32))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var items []store.CatalogItem
	for i := 1; i <= n; i++ {
		items = append(items, store.CatalogItem{ID: fmt.Sprintf("%032x", i), Kind: "movie", Name: fmt.Sprint(i), SortName: fmt.Sprint(i), TMDB: fmt.Sprint(i)})
	}
	if err := st.PutCatalog(context.Background(), items, 1); err != nil {
		t.Fatal(err)
	}
	return st
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// Ten failures in a row: the source itself is unwell, the pass stops
// there with the error, the rest left for the next.
func TestTenFailuresStopThePass(t *testing.T) {
	st := films(t, 15)
	fail := map[int]bool{}
	for i := 1; i <= 15; i++ {
		fail[i] = true
	}
	src := &fakeSource{fail: fail, asked: map[int]int{}}
	k := NewKeeper(func() Source { return src }, st, quiet, 0, 0)
	if err := k.Once(context.Background()); err == nil {
		t.Fatal("no error after ten failures")
	}
	if n := len(src.asked); n != 10 {
		t.Errorf("asked %d titles, want 10", n)
	}
}

// A refused key stops the pass at once, its error said.
func TestRefusedKeyStopsThePass(t *testing.T) {
	st := films(t, 3)
	asked := 0
	src := sourceFunc(func(int) (*tmdb.Release, error) { asked++; return nil, fmt.Errorf("x: %w", tmdb.ErrUnauthorized) })
	k := NewKeeper(func() Source { return src }, st, quiet, 0, 0)
	if err := k.Once(context.Background()); !errors.Is(err, tmdb.ErrUnauthorized) || asked != 1 {
		t.Errorf("%v after %d asks", err, asked)
	}
}

// TMDB taken away during a pass: nothing more fetched nor written, no
// error; no source at all, nothing asked.
func TestSourceRemovedDuringAPass(t *testing.T) {
	st := films(t, 3)
	var mu sync.Mutex
	var current Source
	asked := 0
	current = sourceFunc(func(int) (*tmdb.Release, error) {
		asked++
		mu.Lock()
		current = nil
		mu.Unlock()
		return &tmdb.Release{Ratings: tmdb.Ratings{"US": {"G"}}}, nil
	})
	get := func() Source { mu.Lock(); defer mu.Unlock(); return current }
	k := NewKeeper(get, st, quiet, 0, 0)
	if err := k.Once(context.Background()); err != nil || asked != 1 {
		t.Errorf("%v after %d asks", err, asked)
	}
	if fetched, _, _ := st.RatingProgress(context.Background()); fetched != 1 {
		t.Errorf("fetched %d, want the one answered", fetched)
	}
	if err := NewKeeper(func() Source { return nil }, st, quiet, 0, 0).Once(context.Background()); err != nil {
		t.Errorf("no source: %v", err)
	}
}

// Run makes a pass at once, another when woken, and stops with its
// context.
func TestRunAndWake(t *testing.T) {
	st := films(t, 1)
	passes := make(chan struct{}, 4)
	src := &countingScales{passes: passes}
	k := NewKeeper(func() Source { return src }, st, quiet, time.Hour, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { k.Run(ctx); close(done) }()
	waitPass := func(what string) {
		t.Helper()
		select {
		case <-passes:
		case <-time.After(5 * time.Second):
			t.Fatalf("no pass %s", what)
		}
	}
	waitPass("at start")
	waitPass("at start (series)")
	k.Wake()
	k.Wake() // a second wake while one waits changes nothing
	waitPass("when woken")
	waitPass("when woken (series)")
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run didn't stop with its context")
	}
}

// TMDB as a source: its scales as Overfin keeps them; a title it doesn't
// know, ErrUnknown; other errors as they are.
func TestTMDBAsASource(t *testing.T) {
	src := TMDB{Source: fakeTMDB{}}
	scales, err := src.Scales(context.Background(), "movie")
	if err != nil || len(scales["FR"]) != 2 || scales["FR"][1] != (store.RatingLevel{Certification: "12", Meaning: "12+", Position: 2}) {
		t.Errorf("scales: %+v %v", scales, err)
	}
	if _, err := src.Release(context.Background(), "movie", 404); !errors.Is(err, ErrUnknown) {
		t.Errorf("unknown: %v", err)
	}
	if _, err := src.Release(context.Background(), "movie", 500); err == nil || errors.Is(err, ErrUnknown) {
		t.Errorf("failing: %v", err)
	}
	if r, err := src.Release(context.Background(), "movie", 1); err != nil || r.Released != "2001-01-01" {
		t.Errorf("known: %+v %v", r, err)
	}
	if _, err := (TMDB{Source: fakeTMDB{failScales: true}}).Scales(context.Background(), "movie"); err == nil {
		t.Error("failing scales: no error")
	}
}

type sourceFunc func(int) (*tmdb.Release, error)

func (sourceFunc) Scales(context.Context, string) (map[string][]store.RatingLevel, error) {
	return nil, nil
}
func (f sourceFunc) Release(_ context.Context, _ string, id int) (*tmdb.Release, error) { return f(id) }

type countingScales struct{ passes chan struct{} }

func (c *countingScales) Scales(context.Context, string) (map[string][]store.RatingLevel, error) {
	c.passes <- struct{}{}
	return nil, nil
}
func (c *countingScales) Release(context.Context, string, int) (*tmdb.Release, error) {
	return &tmdb.Release{}, nil
}

type fakeTMDB struct {
	tmdb.Source
	failScales bool
}

func (f fakeTMDB) Certifications(context.Context, string) (map[string][]tmdb.Certification, error) {
	if f.failScales {
		return nil, errors.New("tmdb: 500")
	}
	return map[string][]tmdb.Certification{"FR": {{Certification: "U", Order: 1}, {Certification: "12", Meaning: "12+", Order: 2}}}, nil
}

func (fakeTMDB) Release(_ context.Context, _ string, id int) (*tmdb.Release, error) {
	switch id {
	case 404:
		return nil, fmt.Errorf("x: %w", tmdb.ErrNotFound)
	case 500:
		return nil, errors.New("tmdb: 500")
	}
	return &tmdb.Release{Released: "2001-01-01"}, nil
}
