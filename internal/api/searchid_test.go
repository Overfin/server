package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// fakeIDs is TMDB for searches by ID: it has film 603, series 1399 with
// its first season, and film 604; it knows tt0133093 (603), tt1480055 (an
// episode of 1399) and tvdb 121361 (1399). It counts what it's asked.
type fakeIDs struct {
	fakePeople
	titles, seasons, finds, searches int
}

func (f *fakeIDs) Title(_ context.Context, kind string, id int) (*tmdb.Full, error) {
	f.titles++
	full := &tmdb.Full{}
	switch {
	case kind == "movie" && id == 603:
		full.Title = tmdb.Title{TMDBID: 603, Kind: "movie", Name: "Matrix", Year: 1999}
		full.IMDb = "tt0133093"
		full.Release = &tmdb.Release{Ratings: map[string][]string{"US": {"PG-13"}}, Released: "1999-03-31", AtHome: "1999-09-21"}
	case kind == "movie" && id == 604:
		full.Title = tmdb.Title{TMDBID: 604, Kind: "movie", Name: "Small", Year: 2003}
		full.Release = &tmdb.Release{Ratings: map[string][]string{"US": {"G"}}, Released: "2003-05-15", AtHome: "2003-10-14"}
	case kind == "series" && id == 1399:
		full.Title = tmdb.Title{TMDBID: 1399, Kind: "series", Name: "Le Trône de fer", Year: 2011}
		full.Seasons = []tmdb.Season{{Number: 1, EpisodeCount: 2}}
		full.Release = &tmdb.Release{Ratings: map[string][]string{"US": {"TV-PG"}}, Released: "2011-04-17"}
	default:
		return nil, tmdb.ErrNotFound
	}
	return full, nil
}

func (f *fakeIDs) Season(_ context.Context, series, n int) (*tmdb.SeasonFull, error) {
	f.seasons++
	if series != 1399 || n != 1 {
		return nil, tmdb.ErrNotFound
	}
	return &tmdb.SeasonFull{Number: 1, Name: "Saison 1", AirDate: "2011-04-17", Episodes: []tmdb.Episode{
		{ID: 63056, Number: 1, Name: "L'hiver vient", AirDate: "2011-04-17"},
		{ID: 63057, Number: 2, Name: "La route royale", AirDate: "2011-04-24"}}}, nil
}

func (f *fakeIDs) FindAll(_ context.Context, source, id string) ([]tmdb.Named, error) {
	f.finds++
	switch source + ":" + id {
	case "imdb:tt0133093":
		return []tmdb.Named{{Kind: "movie", TMDB: 603}}, nil
	case "imdb:tt1480055":
		return []tmdb.Named{{Kind: "episode", TMDB: 1399, Episode: 63056, Season: 1, Number: 1}}, nil
	case "tvdb:121361":
		return []tmdb.Named{{Kind: "series", TMDB: 1399}}, nil
	}
	return nil, nil
}

func (f *fakeIDs) Search(ctx context.Context, q string) ([]tmdb.Title, []tmdb.PersonHit, error) {
	f.searches++
	return f.fakePeople.Search(ctx, q)
}

type idResults struct {
	Titles []struct {
		ID, Name, Availability string
		Kind                   string
	}
	People []struct{ ID string }
	ByID   *struct {
		ID            string
		Invalid       bool
		SeriesInstead bool
	}
}

// A search by ID: the title it names, what this server keeps first and
// TMDB only when it doesn't, once; an ID naming nothing, or no ID at all,
// finds nothing and searches no words.
func TestSearchByID(t *testing.T) {
	var st *store.Store
	src := &fakeIDs{}
	srv, _ := newServerWith(t, options{store: &st, tmdb: src})
	ctx := context.Background()
	search := func(q string) idResults {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"query": q})
		code, body := post(t, srv, "/api/v1/search", string(b))
		if code != 200 {
			t.Fatalf("%s: %d %s", q, code, body)
		}
		var out idResults
		json.Unmarshal([]byte(body), &out)
		return out
	}
	ids := func(r idResults) string {
		s := ""
		for _, t := range r.Titles {
			s += t.ID + " " + t.Availability + ";"
		}
		return s
	}

	// The library's film, known by its IMDb ID as Jellyfin has it: found
	// here, TMDB not asked.
	st.PutCatalog(ctx, []store.CatalogItem{{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "Metropolis", SortName: "m",
		TMDB: "19", IMDb: "tt0017136"}}, 1)
	st.RefreshTitleKeys(ctx)
	st.PutTitles(ctx, []store.Title{{Kind: "movie", TMDB: "19", Name: "Metropolis", Year: 1927}}, time.Now())
	r := search("tt0017136")
	if ids(r) != "title_movie_tmdb_19 available;" || r.ByID == nil || r.ByID.ID != "imdb:tt0017136" || len(r.People) != 0 ||
		src.finds+src.titles+src.searches != 0 {
		t.Errorf("the library's film: %+v, asked %+v", r, src)
	}

	// Not kept: TMDB finds it, then its page is fetched (2 requests); the
	// next time, it's kept with its IMDb ID and TMDB isn't asked.
	r = search("https://www.imdb.com/title/tt0133093/")
	if ids(r) != "title_movie_tmdb_603 missing;" || r.Titles[0].Name != "Matrix" || src.finds != 1 || src.titles != 1 {
		t.Errorf("tt0133093: %+v, asked %+v", r, src)
	}
	r = search("imdb:TT0133093")
	if ids(r) != "title_movie_tmdb_603 missing;" || src.finds != 1 || src.titles != 1 {
		t.Errorf("tt0133093 again: %+v, asked %+v", r, src)
	}

	// An ID TMDB doesn't know: nothing, asked once a day.
	for range 2 {
		if r = search("tt9999999"); len(r.Titles) != 0 || r.ByID == nil || r.ByID.Invalid || src.finds != 2 {
			t.Errorf("unknown ID: %+v, asked %+v", r, src)
		}
	}

	// By TMDB's ID: a film, a series, both kinds for a bare number; a link.
	// A TMDB ID nothing has is asked once, then not again for a while.
	if r = search("https://www.themoviedb.org/movie/604-small"); ids(r) != "title_movie_tmdb_604 missing;" || src.titles != 2 {
		t.Errorf("TMDB link: %+v, asked %+v", r, src)
	}
	titles := src.titles
	if r = search("tmdb:604"); ids(r) != "title_movie_tmdb_604 missing;" || src.titles != titles+1 {
		t.Errorf("tmdb:604: %+v, asked %+v", r, src)
	}
	if r = search("tmdb:604"); ids(r) != "title_movie_tmdb_604 missing;" || src.titles != titles+1 {
		t.Errorf("tmdb:604 again: %+v, asked %+v", r, src)
	}

	// An episode by IMDb ID: its series and season fetched, the episode
	// shown alone.
	r = search("tt1480055")
	if ids(r) != "title_series_tmdb_1399_e63056 missing;" || r.Titles[0].Kind != "episode" || r.ByID.SeriesInstead || src.seasons != 1 {
		t.Errorf("an episode: %+v, asked %+v", r, src)
	}
	// By TMDB's numbers of it: the season kept, nothing asked.
	asked := src.titles + src.seasons + src.finds
	if r = search("https://www.themoviedb.org/tv/1399-got/season/1/episode/2"); ids(r) != "title_series_tmdb_1399_e63057 missing;" ||
		src.titles+src.seasons+src.finds != asked {
		t.Errorf("an episode by its numbers: %+v, asked %+v", r, src)
	}
	// One whose season TMDB lacks: its series instead, said so.
	if r = search("tmdb:tv/1399/s7/e1"); ids(r) != "title_series_tmdb_1399 missing;" || !r.ByID.SeriesInstead {
		t.Errorf("an episode not shown alone: %+v", r)
	}
	// By TVDB's ID, for a series.
	if r = search("tvdb:121361"); ids(r) != "title_series_tmdb_1399 missing;" {
		t.Errorf("tvdb: %+v", r)
	}

	// Not an ID: nothing searched, said so.
	searches := src.searches
	for _, q := range []string{"imdb:matrix", "https://www.imdb.com/name/nm0000206/"} {
		if r = search(q); len(r.Titles) != 0 || r.ByID == nil || !r.ByID.Invalid || src.searches != searches {
			t.Errorf("%s: %+v", q, r)
		}
	}
	// Words are searched as ever.
	if r = search("evasion"); r.ByID != nil || src.searches != searches+1 {
		t.Errorf("words: %+v", r)
	}

	// A kids profile: only what's within its ceilings (603 is PG-13, 604
	// G), as any search.
	_, body := post(t, srv, "/api/v1/profiles", `{"name":"Kid","kind":"kids","maxAge":0}`)
	var kid struct{ ID string }
	json.Unmarshal([]byte(body), &kid)
	if code, body := post(t, srv, "/api/v1/profiles/"+kid.ID+"/activate", `{}`); code != 200 {
		t.Fatalf("activate kid: %d %s", code, body)
	}
	if r = search("tt0133093"); len(r.Titles) != 0 {
		t.Errorf("kids, PG-13: %+v", r)
	}
	if r = search("tmdb:movie/604"); ids(r) != "title_movie_tmdb_604 missing;" {
		t.Errorf("kids, G: %+v", r)
	}
}
