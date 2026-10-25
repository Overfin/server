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
		p.Titles = []tmdb.Title{{TMDBID: 1399, Kind: "series", Name: "Le Trône de fer"}}
	}
	return p, nil
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

	// TMDB describes the series, not film 100: only the series is valued,
	// and only its season that aired.
	k := NewTitles(func() TitleSource { return &fakeTitles{} }, st, logger, time.Hour, 0)
	if err := k.Once(ctx); err != nil {
		t.Fatal(err)
	}
	f, err := st.ValuationFacts(ctx, now, nil)
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
