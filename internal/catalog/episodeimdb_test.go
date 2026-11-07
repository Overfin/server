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

// fakeEpisodeIMDb answers episodes' IMDb IDs by their numbers, and counts
// what it's asked.
type fakeEpisodeIMDb struct {
	ids   map[[3]int]string
	asked int
}

func (f *fakeEpisodeIMDb) EpisodeIMDb(_ context.Context, series, season, episode int) (string, error) {
	f.asked++
	id, ok := f.ids[[3]int{series, season, episode}]
	if !ok {
		return "", tmdb.ErrNotFound
	}
	return id, nil
}

// Each library episode's IMDb ID is asked once, only while IMDb's data is
// on; a season's valued episodes then carry them, by TMDB's ID.
func TestEpisodeIMDb(t *testing.T) {
	box, _ := seal.New(bytes.Repeat([]byte{9}, 32))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	one, two := 1, 2
	st.PutCatalog(ctx, []store.CatalogItem{{ID: id(1), Kind: "series", Name: "GoT", SortName: "got", TMDB: "1399"},
		{ID: id(2), Kind: "episode", Name: "E1", SortName: "e1", SeriesID: id(1), SeasonNumber: &one, EpisodeNumber: &one},
		{ID: id(3), Kind: "episode", Name: "E2", SortName: "e2", SeriesID: id(1), SeasonNumber: &one, EpisodeNumber: &two}}, 1)
	st.RefreshTitleKeys(ctx)
	season := store.Season{Number: 1, Name: "Season 1", Episodes: []store.Episode{
		{ID: 63056, Number: 1, Name: "Winter", AirDate: "2011-04-17"},
		{ID: 63057, Number: 2, Name: "Kingsroad", AirDate: "2011-04-24"},
		{ID: 63058, Number: 3, Name: "Lord Snow", AirDate: "2011-05-01"},
	}}
	if err := st.SetSeason(ctx, "1399", season, time.Now()); err != nil {
		t.Fatal(err)
	}
	// A series no library has: never asked.
	if err := st.SetSeason(ctx, "1400", store.Season{Number: 1, Name: "S1", Episodes: []store.Episode{{ID: 1, Number: 1, Name: "Pilot"}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	src := &fakeEpisodeIMDb{ids: map[[3]int]string{{1399, 1, 1}: "tt1480055", {1399, 1, 2}: "tt1668746", {1399, 1, 3}: ""}}
	on := false
	job := &EpisodeIMDb{Store: st, Source: func() EpisodeIMDbSource { return src }, On: func(context.Context) (bool, error) { return on, nil },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := job.Once(ctx); err != nil || src.asked != 0 {
		t.Fatalf("off: asked %d, %v", src.asked, err)
	}
	on = true
	if err := job.Once(ctx); err != nil || src.asked != 3 {
		t.Fatalf("on: asked %d, %v", src.asked, err)
	}
	// Asked once: not again until TMDB's data is old.
	if err := job.Once(ctx); err != nil || src.asked != 3 {
		t.Errorf("again: asked %d, %v", src.asked, err)
	}
	job.now = func() time.Time { return time.Now().Add(TitlesFresh + time.Hour) }
	if err := job.Once(ctx); err != nil || src.asked != 6 {
		t.Errorf("once old: asked %d, %v", src.asked, err)
	}
	job.now = nil

	f, err := st.ValuationFacts(ctx, time.Now(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	s1 := f.Titles["series/tmdb/1399/s1"]
	if s1 == nil || len(s1.EpisodesIMDb) != 3 || s1.EpisodesIMDb[0] != "tt1480055" || s1.EpisodesIMDb[1] != "tt1668746" || s1.EpisodesIMDb[2] != "" {
		t.Errorf("season 1: %+v", s1)
	}
	todo, err := st.EpisodesToLink(ctx, time.Now().Add(-time.Hour), 10)
	if err != nil || len(todo) != 0 {
		t.Errorf("left: %+v %v", todo, err)
	}
}
