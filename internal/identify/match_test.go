package identify

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/overfin/server/internal/tmdb"
)

// fakeTMDB answers matching's questions from what it's given: searches by
// kind, name and year (0: any), pages by kind and ID, IDs by source.
type fakeTMDB struct {
	searches map[string][]tmdb.Found // "movie/heat/1995"
	pages    map[string]*tmdb.Full   // "movie/949"
	finds    map[string]int          // "imdb/tt0245429"
	asked    []string
}

func (f *fakeTMDB) SearchKind(_ context.Context, kind, query string, year int) ([]tmdb.Found, error) {
	key := kind + "/" + strings.ToLower(query) + "/" + strconv.Itoa(year)
	f.asked = append(f.asked, "search "+key)
	return f.searches[key], nil
}

func (f *fakeTMDB) Title(_ context.Context, kind string, id int) (*tmdb.Full, error) {
	f.asked = append(f.asked, "page "+kind+"/"+strconv.Itoa(id))
	if p := f.pages[kind+"/"+strconv.Itoa(id)]; p != nil {
		return p, nil
	}
	return nil, tmdb.ErrNotFound
}

func (f *fakeTMDB) Find(_ context.Context, _, source, id string) (int, error) {
	if n := f.finds[source+"/"+id]; n > 0 {
		return n, nil
	}
	return 0, tmdb.ErrNotFound
}

func film(id int, name string, year, runtime int, language string, names ...string) *tmdb.Full {
	f := &tmdb.Full{Runtime: runtime, Names: names}
	f.TMDBID, f.Kind, f.Name, f.Year, f.OriginalName, f.Language = id, "movie", name, year, name, language
	return f
}

func series(id int, name string, year int, language string, seasons ...int) *tmdb.Full {
	f := &tmdb.Full{}
	f.TMDBID, f.Kind, f.Name, f.Year, f.OriginalName, f.Language = id, "series", name, year, name, language
	for i, n := range seasons {
		f.Seasons = append(f.Seasons, tmdb.Season{Number: i + 1, EpisodeCount: n})
	}
	return f
}

func found(f *tmdb.Full, votes int) tmdb.Found { return tmdb.Found{Title: f.Title, Votes: votes} }

// The cases Identification caught on a real library, matched right from
// the start: by name and year, by a film's length, by a series' seasons,
// by its language; sure when one clearly fits, said when not.
func TestMatch(t *testing.T) {
	heat, heat86 := film(949, "Heat", 1995, 170, "en"), film(10000, "Heat", 1986, 101, "en")
	spirited, special := film(129, "Spirited Away", 2001, 125, "ja", "Le Voyage de Chihiro"), film(535075, "Spirited Away", 2001, 48, "ja")
	quebec, remake := series(38527, "19-2", 2011, "fr", 10, 10, 10), series(61546, "19-2", 2014, "en", 10, 10, 10, 8)
	titan, chibi := series(1429, "Attack on Titan", 2013, "ja", 25, 12, 22, 28), series(224499, "Attack on Titan", 2013, "ja", 10)
	swedish, odyssey := film(1261188, "The Swedish Connection", 2026, 95, "sv"), film(1368337, "The Odyssey", 2026, 150, "en")
	src := &fakeTMDB{
		pages: map[string]*tmdb.Full{"movie/949": heat, "movie/10000": heat86, "movie/129": spirited, "movie/535075": special,
			"series/38527": quebec, "series/61546": remake, "series/1429": titan, "series/224499": chibi,
			"movie/1261188": swedish, "movie/1368337": odyssey},
		searches: map[string][]tmdb.Found{
			"movie/heat/1995":                   {found(heat, 8000)},
			"movie/spirited away/2001":          {found(special, 40), found(spirited, 17000)},
			"series/19-2/0":                     {found(remake, 300), found(quebec, 40)},
			"series/attack on titan/0":          {found(chibi, 60), found(titan, 7000)},
			"movie/the swedish connection/2026": {found(odyssey, 900), found(swedish, 20)},
			"movie/a film nobody made/0":        nil,
			"movie/heat/1990":                   nil,
			"movie/heat/0":                      {found(heat, 8000), found(heat86, 20)},
		},
		finds: map[string]int{"imdb/tt0245429": 129},
	}
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		f    Files
		want string
		sure bool
	}{
		{"name and year", Files{Kind: "movie", Folder: "Heat (1995)", RuntimeMs: 170 * 60_000}, "949", true},
		{"a film's length over a special", Files{Kind: "movie", Folder: "Spirited Away (2001)", RuntimeMs: 125 * 60_000}, "129", true},
		{"the audio's language", Files{Kind: "series", Folder: "19-2", Audio: []string{"fre"}, Episodes: map[int]int{1: 10, 2: 10, 3: 10}}, "38527", false},
		{"a series' seasons over a spin-off", Files{Kind: "series", Folder: "Attack on Titan", Episodes: map[int]int{1: 25, 2: 12, 3: 22}}, "1429", true},
		{"the name over the more known", Files{Kind: "movie", Folder: "The Swedish Connection (2026)", RuntimeMs: 95 * 60_000}, "1261188", true},
		{"a year off by a few: searched again without it", Files{Kind: "movie", Folder: "Heat (1990)"}, "949", false},
		{"an IMDb ID in the name", Files{Kind: "movie", Folder: "Le Voyage de Chihiro [imdbid-tt0245429]", RuntimeMs: 125 * 60_000}, "129", true},
	} {
		got, err := Match(ctx, src, tc.f)
		if err != nil || got.TMDB != tc.want || got.Sure != tc.sure {
			t.Errorf("%s: %+v %v, want %s sure=%v", tc.name, got, err, tc.want, tc.sure)
		}
		if !got.Sure && len(got.Reasons) == 0 {
			t.Errorf("%s: unsure without a reason", tc.name)
		}
	}

	// A TMDB ID in the name is taken without a search, and what the files
	// contradict is said, not undone.
	src.asked = nil
	got, err := Match(ctx, src, Files{Kind: "movie", Folder: "Heat (1995) [tmdbid-949]", RuntimeMs: 60 * 60_000})
	if err != nil || got.TMDB != "949" || got.Sure || len(got.Reasons) != 1 || got.Reasons[0].Code != "runtime" {
		t.Errorf("ID contradicted: %+v %v", got, err)
	}
	for _, a := range src.asked {
		if strings.HasPrefix(a, "search") {
			t.Errorf("searched although the ID was there: %v", src.asked)
		}
	}
	// A file under a minute long says nothing of its length.
	if got, _ := Match(ctx, src, Files{Kind: "movie", Folder: "Heat (1995) [tmdbid-949]", RuntimeMs: 20_000}); !got.Sure {
		t.Errorf("20 seconds: %+v", got)
	}
	// One TMDB doesn't know is searched for instead.
	got, _ = Match(ctx, src, Files{Kind: "movie", Folder: "Heat (1995) [tmdbid-424242]"})
	if got.TMDB != "949" {
		t.Errorf("unknown ID: %+v", got)
	}
	// Nothing found, nothing to search: no title, and why.
	for _, f := range []Files{{Kind: "movie", Folder: "A Film Nobody Made"}, {Kind: "movie", Folder: "[1080p]"}} {
		if got, err := Match(ctx, src, f); err != nil || got.TMDB != "" || got.Sure || len(got.Reasons) != 1 {
			t.Errorf("%q: %+v %v", f.Folder, got, err)
		}
	}
	if _, err := Match(ctx, src, Files{Kind: "episode"}); err == nil {
		t.Error("an episode matched")
	}
}
