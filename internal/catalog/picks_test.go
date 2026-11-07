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

// fakePicks is TMDB for the picks: a film like the library's film 7 (and
// film 7 itself), and a series in genre 18.
type fakePicks struct {
	asked []string
}

func (f *fakePicks) Recommendations(_ context.Context, kind string, id int) (*tmdb.Page, error) {
	f.asked = append(f.asked, "like "+kind)
	if kind != "movie" || id != 7 {
		return nil, tmdb.ErrNotFound
	}
	return &tmdb.Page{Page: 1, TotalPages: 1, Titles: []tmdb.Title{
		{TMDBID: 100, Kind: "movie", Name: "Pick"}, {TMDBID: 7, Kind: "movie", Name: "Film"}}}, nil
}

func (f *fakePicks) ByGenre(_ context.Context, kind string, genre, page int) (*tmdb.Page, error) {
	f.asked = append(f.asked, "genre "+kind)
	p := &tmdb.Page{Page: page, TotalPages: 1}
	if kind == "series" && genre == 18 {
		p.Titles = []tmdb.Title{{TMDBID: 1399, Kind: "series", Name: "Le Trône de fer", ListGenres: []int{18, 10767}}}
	}
	return p, nil
}

func (f *fakePicks) Providers(_ context.Context, kind, country string, _ int) ([]int, error) {
	f.asked = append(f.asked, "providers "+kind+" "+country)
	if kind == "series" {
		return nil, nil // none known: nothing asked of series
	}
	return []int{8}, nil
}

func (f *fakePicks) Streaming(_ context.Context, kind, country string, providers []int, _ time.Time, page int) (*tmdb.Page, error) {
	f.asked = append(f.asked, "streaming "+kind+" "+country)
	return &tmdb.Page{Page: page, TotalPages: 1, Titles: []tmdb.Title{{TMDBID: 200, Kind: "movie", Name: "Streams here"}}}, nil
}

func (f *fakePicks) OutAtHome(_ context.Context, country string, _ time.Time, page int) (*tmdb.Page, error) {
	f.asked = append(f.asked, "home "+country)
	return &tmdb.Page{Page: page, TotalPages: 1, Titles: []tmdb.Title{{TMDBID: 201, Kind: "movie", Name: "Out at home here"}, {TMDBID: 200, Kind: "movie", Name: "Streams here"}}}, nil
}

// The picks' lists come from what people watch and the genres they like;
// the titles keeper fetches the pages of those the library lacks, and the
// valuation gets the described ones: a series as its aired seasons.
func TestPicks(t *testing.T) {
	box, _ := seal.New(bytes.Repeat([]byte{9}, 32))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now()
	acct, prof, err := st.CreateAccount(ctx, "subject-1", store.NewOwner{Name: "Me", JellyfinUserID: "jf-1", JellyfinUsername: "of-1", JellyfinToken: "token-1"})
	if err != nil {
		t.Fatal(err)
	}
	st.PutCatalog(ctx, []store.CatalogItem{{ID: id(1), Kind: "movie", Name: "Film", SortName: "film", TMDB: "7"}}, 1)
	st.RefreshTitleKeys(ctx)
	if err := st.RecordPlay(ctx, prof.ID, "movie/tmdb/7", id(1), 1000, false, now); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTastesAsked(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountTastes(ctx, acct.ID, store.Tastes{Genres: []int{18}}, now); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	src := &fakePicks{}
	if err := NewPicks(func() PickSource { return src }, st, logger, time.Hour, 0).Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	liked, _ := st.ListTitles(ctx, "recommended", "movie", "missing", nil, 0, 10)
	if len(liked) != 1 || liked[0].TMDB != "100" {
		t.Errorf("recommended, missing: %+v", liked)
	}
	series, _ := st.ListTitles(ctx, "liked", "series", "", nil, 0, 10)
	if len(series) != 1 || series[0].TMDB != "1399" {
		t.Errorf("liked series: %+v (asked %v)", series, src.asked)
	}
	// What's out in the server's country (its ratings country): on its
	// streaming services, and released at home there, each film once.
	regional, _ := st.ListTitles(ctx, "regional", "movie", "", nil, 0, 10)
	if len(regional) != 2 || regional[0].TMDB != "200" || regional[1].TMDB != "201" {
		t.Errorf("regional: %+v", regional)
	}
	for _, want := range []string{"providers movie US", "streaming movie US", "providers series US", "home US"} {
		if !slices.Contains(src.asked, want) {
			t.Errorf("not asked %q: %v", want, src.asked)
		}
	}
	if slices.Contains(src.asked, "streaming series US") {
		t.Errorf("series asked without providers: %v", src.asked)
	}

	// TMDB describes the series, not film 100: only the series is valued,
	// and only its season that aired.
	k := NewTitles(func() TitleSource { return &fakeTitles{} }, st, logger, time.Hour, 0)
	if err := k.Once(ctx); err != nil {
		t.Fatal(err)
	}
	f, err := st.ValuationFacts(ctx, now, nil, 60)
	if err != nil {
		t.Fatal(err)
	}
	s1 := f.Titles["series/tmdb/1399/s1"]
	if s1 == nil || s1.InLibrary || s1.Released.Format(time.DateOnly) != "2011-04-17" || s1.Name != "Le Trône de fer" || s1.TVDB != "121361" {
		t.Errorf("season 1: %+v", s1)
	}
	if f.Titles["series/tmdb/1399/s2"] != nil || f.Titles["movie/tmdb/100"] != nil {
		t.Errorf("an unaired season or an undescribed film is valued: %v", f.Titles)
	}
	if film := f.Titles["movie/tmdb/7"]; film == nil || !film.InLibrary {
		t.Errorf("the library's film: %+v", film)
	}

	// A genre left out (as its list gave it): the series isn't valued,
	// nor described again; the library's film still is.
	if err := st.SetGenresLeftOut(ctx, store.GenresLeftOut{Series: []int{10767}}); err != nil {
		t.Fatal(err)
	}
	if f, err = st.ValuationFacts(ctx, now, nil, 60); err != nil {
		t.Fatal(err)
	}
	if f.Titles["series/tmdb/1399/s1"] != nil || f.Titles["movie/tmdb/7"] == nil {
		t.Errorf("left out: %v", f.Titles)
	}
	todo, err := st.PicksToDescribe(ctx, now.Add(time.Hour), 10)
	if err != nil || slices.ContainsFunc(todo, func(x store.ArtworkTarget) bool { return x.TMDB == "1399" }) {
		t.Errorf("to describe: %v %v", todo, err)
	}
	st.SetGenresLeftOut(ctx, store.GenresLeftOut{})

	// Without asking people, nothing is liked any more.
	if err := st.SetTastesAsked(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := NewPicks(func() PickSource { return src }, st, logger, time.Hour, 0).Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if series, _ := st.ListTitles(ctx, "liked", "series", "", nil, 0, 10); len(series) != 0 {
		t.Errorf("liked, not asked: %+v", series)
	}
}
