package imdb

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// IMDb's order is the IDs' as text: an eight-digit ID comes between
// seven-digit ones (tt10000000 after tt1000000, before tt1000001).
const (
	basics = "tconst\ttitleType\tprimaryTitle\toriginalTitle\tisAdult\tstartYear\tendYear\truntimeMinutes\tgenres\n" +
		"tt0000001\tshort\tA short\tA short\t0\t1894\t\\N\t1\tShort\n" +
		"tt0133093\tmovie\tThe Matrix\tThe Matrix\t0\t1999\t\\N\t136\tAction\n" +
		"tt0903747\ttvSeries\tBreaking Bad\tBreaking Bad\t0\t2008\t2013\t49\tDrama\n" +
		"tt0959621\ttvEpisode\tPilot\tPilot\t0\t2008\t\\N\t58\tDrama\n" +
		"tt1000000\ttvEpisode\tSeven\tSeven\t0\t2008\t\\N\t\\N\tDrama\n" +
		"tt10000000\ttvEpisode\tEight\tEight\t0\t2009\t\\N\t47\tDrama\n" +
		"tt1000001\tmovie\tAdult\tAdult\t1\t\\N\t\\N\t90\tAdult\n" +
		"tt1054606\ttvEpisode\tSeason two\tSeason two\t0\t2009\t\\N\t47\tDrama\n"
	episodes = "tconst\tparentTconst\tseasonNumber\tepisodeNumber\n" +
		"tt0959621\ttt0903747\t1\t1\n" +
		"tt1000000\ttt0903747\t1\t2\n" +
		"tt10000000\ttt0903747\t1\t3\n" +
		"tt1054606\ttt0903747\t2\t1\n" +
		"tt9999999\ttt0903747\t9\t9\n" // not in basics: skipped
	ratings = "tconst\taverageRating\tnumVotes\n" +
		"tt0000001\t5.7\t2100\n" +
		"tt0133093\t8.7\t2200000\n" +
		"tt0903747\t9.5\t2300000\n" +
		"tt0959621\t9.0\t300\n" +
		"tt10000000\t8.0\t100\n" +
		"tt1054606\t8.5\t50\n"
)

func build(t *testing.T, basics, episodes, ratings string) (*Data, int) {
	t.Helper()
	var out bytes.Buffer
	n, err := Build(&out, strings.NewReader(basics), strings.NewReader(episodes), strings.NewReader(ratings), time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "imdb")
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d, n
}

func TestBuildAndFind(t *testing.T) {
	d, n := build(t, basics, episodes, ratings)
	if n != 7 || d.Titles != 7 || d.Built != 1700000000 {
		t.Errorf("titles %d %d, built %d", n, d.Titles, d.Built)
	}
	want := map[string]Title{
		"tt0133093":  {ID: "tt0133093", Kind: Movie, Start: 1999, Minutes: 136, Rating: 8.7, Votes: 2200000},
		"tt0903747":  {ID: "tt0903747", Kind: Series, Start: 2008, End: 2013, Minutes: 49, Rating: 9.5, Votes: 2300000},
		"tt0959621":  {ID: "tt0959621", Kind: Episode, Start: 2008, Minutes: 58, Rating: 9, Votes: 300, Series: "tt0903747", Season: 1, Number: 1},
		"tt1000000":  {ID: "tt1000000", Kind: Episode, Start: 2008, Series: "tt0903747", Season: 1, Number: 2},
		"tt10000000": {ID: "tt10000000", Kind: Episode, Start: 2009, Minutes: 47, Rating: 8, Votes: 100, Series: "tt0903747", Season: 1, Number: 3},
		"tt1000001":  {ID: "tt1000001", Kind: Movie, Adult: true, Minutes: 90},
	}
	for id, w := range want {
		if got, ok := d.Title(id); !ok || got != w {
			t.Errorf("%s: %+v %v", id, got, ok)
		}
	}
	// Shorts aren't kept; neither is what basics doesn't have.
	for _, id := range []string{"tt0000001", "tt9999999", "tt0000002", "tt99999999", "nm0000001", "tt", ""} {
		if got, ok := d.Title(id); ok {
			t.Errorf("%s found: %+v", id, got)
		}
	}
	// Season 1: (9.0×300 + 8.0×100) / 400; the episode without a rating
	// doesn't count.
	if s, ok := d.Season("tt0903747", 1); !ok || s != (Season{Rating: 8.75, Votes: 400, Episodes: 2}) {
		t.Errorf("season 1: %+v %v", s, ok)
	}
	if s, ok := d.Season("tt0903747", 2); !ok || s != (Season{Rating: 8.5, Votes: 50, Episodes: 1}) {
		t.Errorf("season 2: %+v %v", s, ok)
	}
	for _, k := range []struct {
		series string
		season int
	}{{"tt0903747", 3}, {"tt0903747", 9}, {"tt0133093", 1}, {"tt0000001", 1}} {
		if s, ok := d.Season(k.series, k.season); ok {
			t.Errorf("%s season %d: %+v", k.series, k.season, s)
		}
	}
	d.Close()
	if _, ok := d.Title("tt0133093"); ok {
		t.Error("found after Close")
	}
}

// Many titles and seasons: lookups across blocks, a series' seasons never
// split between two.
func TestManyBlocks(t *testing.T) {
	var b, e, r strings.Builder
	b.WriteString("tconst\ttitleType\tisAdult\tstartYear\tendYear\truntimeMinutes\n")
	e.WriteString("tconst\tparentTconst\tseasonNumber\tepisodeNumber\n")
	r.WriteString("tconst\taverageRating\tnumVotes\n")
	// Series 1000000…1000999, each with 3 seasons of 2 episodes: IDs
	// 2000000 on, in order as text (all seven digits).
	ep := 2000000
	for s := range 1000 {
		fmt.Fprintf(&b, "tt%07d\ttvSeries\t0\t2000\t\\N\t30\n", 1000000+s)
	}
	for s := range 1000 {
		for season := 1; season <= 3; season++ {
			for n := 1; n <= 2; n++ {
				fmt.Fprintf(&b, "tt%07d\ttvEpisode\t0\t2000\t\\N\t30\n", ep)
				fmt.Fprintf(&e, "tt%07d\ttt%07d\t%d\t%d\n", ep, 1000000+s, season, n)
				fmt.Fprintf(&r, "tt%07d\t%d.0\t%d\n", ep, season+n, 10*n)
				ep++
			}
		}
	}
	d, n := build(t, b.String(), e.String(), r.String())
	if n != 7000 || len(d.titles) < 7000/blockSize {
		t.Fatalf("%d titles in %d blocks", n, len(d.titles))
	}
	for _, s := range []int{0, 1, 255, 256, 511, 999} {
		series := fmt.Sprintf("tt%07d", 1000000+s)
		if got, ok := d.Title(series); !ok || got.Kind != Series {
			t.Errorf("%s: %+v %v", series, got, ok)
		}
		for season := 1; season <= 3; season++ {
			// Episodes rated season+1 (10 votes) and season+2 (20).
			want := Season{Rating: (float64(season+1)*10 + float64(season+2)*20) / 30, Votes: 30, Episodes: 2}
			got, ok := d.Season(series, season)
			if !ok || got.Votes != want.Votes || got.Episodes != 2 || got.Rating < want.Rating-0.01 || got.Rating > want.Rating+0.01 {
				t.Errorf("%s season %d: %+v %v", series, season, got, ok)
			}
		}
	}
	last := fmt.Sprintf("tt%07d", ep-1)
	if got, ok := d.Title(last); !ok || got.Season != 3 || got.Number != 2 || got.Rating != 5 {
		t.Errorf("last episode: %+v %v", got, ok)
	}
}

func TestBuildRefuses(t *testing.T) {
	var out bytes.Buffer
	unsorted := strings.Replace(ratings, "tt0133093\t8.7", "tt9999999\t8.7", 1)
	if _, err := Build(&out, strings.NewReader(basics), strings.NewReader(episodes), strings.NewReader(unsorted), time.Now()); err == nil {
		t.Error("unsorted ratings were taken")
	}
	if _, err := Build(&out, strings.NewReader("tconst\ttitleType\n"), strings.NewReader(episodes), strings.NewReader(ratings), time.Now()); err == nil {
		t.Error("basics without its columns were taken")
	}
	if _, err := Build(&out, strings.NewReader(""), strings.NewReader(episodes), strings.NewReader(ratings), time.Now()); err == nil {
		t.Error("an empty file was taken")
	}
}

func TestOpenRefusesOtherFiles(t *testing.T) {
	dir := t.TempDir()
	var good bytes.Buffer
	if _, err := Build(&good, strings.NewReader(basics), strings.NewReader(episodes), strings.NewReader(ratings), time.Now()); err != nil {
		t.Fatal(err)
	}
	broken := bytes.Clone(good.Bytes())
	// The footer's offsets, garbled.
	for i := len(broken) - footerSize; i < len(broken)-footerSize+16; i++ {
		broken[i] = 0xff
	}
	for name, b := range map[string][]byte{
		"empty":     nil,
		"text":      []byte("not a file of IMDb's data, but long enough to have a footer, surely"),
		"broken":    broken,
		"truncated": good.Bytes()[:good.Len()/2],
	} {
		path := filepath.Join(dir, name)
		os.WriteFile(path, b, 0o600)
		if d, err := Open(path); err == nil {
			d.Close()
			t.Errorf("%s opened", name)
		}
	}
}

// The real datasets, when IMDB_DATASETS names a folder with them: how long
// building takes, and a few known titles.
func TestRealDatasets(t *testing.T) {
	dir := os.Getenv("IMDB_DATASETS")
	if dir == "" {
		t.Skip("IMDB_DATASETS is the folder of IMDb's three datasets")
	}
	path := filepath.Join(t.TempDir(), "imdb")
	start := time.Now()
	n, err := BuildFiles(path, filepath.Join(dir, BasicsFile), filepath.Join(dir, EpisodesFile), filepath.Join(dir, RatingsFile), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	t.Logf("%d titles, %.1f MB, built in %s", n, float64(st.Size())/1e6, time.Since(start).Round(time.Second))
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	m, ok := d.Title("tt0133093")
	t.Logf("The Matrix: %+v %v", m, ok)
	s, ok := d.Season("tt0903747", 5)
	t.Logf("Breaking Bad season 5: %+v %v", s, ok)
	if m.Kind != Movie || m.Start != 1999 || m.Votes < 1000000 || !ok || s.Episodes < 10 {
		t.Error("not what IMDb says")
	}
}

// The day's votes from the real ratings, when IMDB_DATASETS names their
// folder: how large a day is.
func TestRealVotes(t *testing.T) {
	dir := os.Getenv("IMDB_DATASETS")
	if dir == "" {
		t.Skip("IMDB_DATASETS is the folder of IMDb's three datasets")
	}
	out := t.TempDir()
	n, err := WriteVotes(out, filepath.Join(dir, RatingsFile), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	days, _ := VoteDays(out)
	st, _ := os.Stat(filepath.Join(out, votesPrefix+days[0]))
	t.Logf("%d titles' votes, %.1f MB", n, float64(st.Size())/1e6)
}

// Each day's votes are kept, read back as they were, and days past
// VotesKept go.
func TestVotes(t *testing.T) {
	dir := t.TempDir()
	gz := filepath.Join(dir, RatingsFile+".in")
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	io.WriteString(z, ratings)
	z.Close()
	os.WriteFile(gz, b.Bytes(), 0o600)

	day := time.Date(2026, 11, 7, 18, 0, 0, 0, time.UTC)
	old := day.AddDate(0, 0, -VotesKept-1).Format(time.DateOnly)
	os.WriteFile(filepath.Join(dir, votesPrefix+old), nil, 0o600)
	n, err := WriteVotes(dir, gz, day)
	if err != nil || n != 6 {
		t.Fatalf("%d, %v", n, err)
	}
	if days, _ := VoteDays(dir); len(days) != 1 || days[0] != "2026-11-07" {
		t.Errorf("days: %v", days)
	}
	got := map[string]int{}
	if err := ReadVotes(dir, "2026-11-07", func(id string, votes int) { got[id] = votes }); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"tt0000001": 2100, "tt0133093": 2200000, "tt0903747": 2300000, "tt0959621": 300, "tt10000000": 100, "tt1054606": 50}
	if len(got) != len(want) {
		t.Errorf("votes: %v", got)
	}
	for id, v := range want {
		if got[id] != v {
			t.Errorf("%s: %d, not %d", id, got[id], v)
		}
	}
}
