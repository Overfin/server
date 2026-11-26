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

// orderedTitles is fakeTitles, its episodes with IDs (101, 102; 201), with
// another order: the first of season 2, then one of season 1.
type orderedTitles struct {
	fakeTitles
	gone bool // the order deleted from TMDB (orders are its users')
}

func (o *orderedTitles) withIDs(se *tmdb.SeasonFull) *tmdb.SeasonFull {
	for i := range se.Episodes {
		se.Episodes[i].ID = se.Number*100 + se.Episodes[i].Number
	}
	return se
}

func (o *orderedTitles) Season(ctx context.Context, series, n int) (*tmdb.SeasonFull, error) {
	se, err := o.fakeTitles.Season(ctx, series, n)
	if err != nil {
		return nil, err
	}
	return o.withIDs(se), nil
}

func (o *orderedTitles) Seasons(ctx context.Context, series int, numbers []int) (map[int]*tmdb.SeasonFull, error) {
	out := map[int]*tmdb.SeasonFull{}
	for _, n := range numbers {
		se, err := o.Season(ctx, series, n)
		if err != nil {
			return nil, err
		}
		out[n] = se
	}
	return out, nil
}

func (o *orderedTitles) OtherOrders(context.Context, int) ([]tmdb.EpisodeOrder, error) {
	return []tmdb.EpisodeOrder{{ID: "abc", Name: "Production", Type: 6}}, nil
}

func (o *orderedTitles) ArrangedOrder(_ context.Context, _ int, id string) (*tmdb.ArrangedSeasons, error) {
	if id != "abc" || o.gone {
		return nil, tmdb.ErrNotFound
	}
	return &tmdb.ArrangedSeasons{Name: "Production", Seasons: map[int]*tmdb.SeasonFull{
		1: {Number: 1, Name: "Season 1", Episodes: []tmdb.Episode{{ID: 201, Number: 1, Name: "Other first",
			Guests: []tmdb.Person{{ID: 7, Name: "A guest", Character: "Someone"}}}}},
		3: {Number: 3, Name: "Volume 3", Episodes: []tmdb.Episode{{ID: 101, Number: 1, Name: "Other last"}}},
	}}, nil
}

func TestChooseOrderAndKeepIt(t *testing.T) {
	box, _ := seal.New(bytes.Repeat([]byte{9}, 32))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	one := 1
	st.PutCatalog(ctx, []store.CatalogItem{
		{ID: id(1), Kind: "series", Name: "GoT", SortName: "got", TMDB: "1399"},
		{ID: id(2), Kind: "episode", Name: "E", SortName: "e", SeriesID: id(1), SeasonNumber: &one, EpisodeNumber: &one},
	}, 1)
	src := &orderedTitles{}
	k := NewTitles(func() TitleSource { return src }, st, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour, 0)
	if err := k.Once(ctx); err != nil {
		t.Fatal(err)
	}
	orders, err := k.ReadOrders(ctx, 1399)
	if err != nil || len(orders) != 2 || orders[0].ID != "" || orders[1].ID != "abc" || len(orders[0].Seasons) == 0 {
		t.Fatalf("orders: %+v %v", orders, err)
	}
	if err := k.ChooseOrder(ctx, 1399, "nope"); err == nil {
		t.Error("an order TMDB doesn't list was chosen")
	}
	if err := k.ChooseOrder(ctx, 1399, "abc"); err != nil {
		t.Fatal(err)
	}
	check := func(when string) {
		t.Helper()
		if keys, _ := st.TitleKeys(ctx, []string{id(2)}); keys[id(2)] != "series/tmdb/1399/e101" {
			t.Errorf("%s: file %v", when, keys)
		}
		se, err := st.SeasonByTMDB(ctx, "1399", 1)
		if err != nil || se == nil || se.Name != "Saison 1" || se.Episodes[0].Name != "Other first" {
			t.Errorf("%s: season 1: %+v %v", when, se, err)
		}
		if se, _ := st.SeasonByTMDB(ctx, "1399", 2); se != nil {
			t.Errorf("%s: a season the order hasn't: %+v", when, se)
		}
		if se, _ := st.SeasonByTMDB(ctx, "1399", 3); se == nil || se.Name != "Volume 3" {
			t.Errorf("%s: season 3: %+v", when, se)
		}
		// The file that was S1E1 is shown where the order has its episode.
		ep, err := st.TitleInfos(ctx, []string{"series/tmdb/1399/e101"}, "2030-01-01", "")
		if err != nil || ep["series/tmdb/1399/e101"].Ref.Season != 3 || len(ep["series/tmdb/1399/e101"].Items) != 1 {
			t.Errorf("%s: episode: %+v %v", when, ep["series/tmdb/1399/e101"], err)
		}
		infos, err := st.TitleInfos(ctx, []string{"series/tmdb/1399"}, "2030-01-01", "")
		if err != nil || len(infos["series/tmdb/1399"].Details.Seasons) != 2 {
			t.Errorf("%s: details: %+v %v", when, infos["series/tmdb/1399"], err)
		}
	}
	check("chosen")
	// The keeper's next pass reads the order, not TMDB's own seasons.
	if err := st.SetArtwork(ctx, store.ArtworkTarget{Kind: "series", TMDB: "1399"}, nil, time.Now().Add(-TitlesFresh-time.Hour)); err != nil {
		t.Fatal(err)
	}
	k2 := NewTitles(func() TitleSource { return src }, st, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour, 0)
	if err := k2.Once(ctx); err != nil {
		t.Fatal(err)
	}
	check("refreshed")
	// The order deleted from TMDB: what's kept stays as it is, its
	// episodes' people too.
	src.gone = true
	if err := st.SetArtwork(ctx, store.ArtworkTarget{Kind: "series", TMDB: "1399"}, nil, time.Now().Add(-TitlesFresh-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := k2.Once(ctx); err != nil {
		t.Fatal(err)
	}
	check("the order gone")
	if se, _ := st.SeasonByTMDB(ctx, "1399", 1); se == nil || len(se.Episodes[0].Guests) != 1 || se.Episodes[0].Guests[0].TMDB != "7" {
		t.Errorf("the order gone: season 1's guests: %+v", se)
	}
	if group, _, _ := st.SeriesOrder(ctx, "1399"); group != "abc" {
		t.Errorf("the order gone: chosen %q", group)
	}
	src.gone = false
	// Back to TMDB's own.
	if err := k.ChooseOrder(ctx, 1399, ""); err != nil {
		t.Fatal(err)
	}
	if group, _, _ := st.SeriesOrder(ctx, "1399"); group != "" {
		t.Errorf("order: %q", group)
	}
	if se, _ := st.SeasonByTMDB(ctx, "1399", 2); se == nil {
		t.Error("TMDB's season 2 is back: not")
	}
}
