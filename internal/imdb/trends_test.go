package imdb

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// votesOn keeps a day's votes in dir, as a download of IMDb's ratings
// would (id → votes).
func votesOn(t *testing.T, dir, day string, votes map[string]int) {
	t.Helper()
	ids := make([]string, 0, len(votes))
	for id := range votes {
		ids = append(ids, id)
	}
	slices.Sort(ids) // IMDb's order: as text
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	fmt.Fprint(z, "tconst\taverageRating\tnumVotes\n")
	for _, id := range ids {
		fmt.Fprintf(z, "%s\t7.0\t%d\n", id, votes[id])
	}
	z.Close()
	in := filepath.Join(t.TempDir(), RatingsFile)
	if err := os.WriteFile(in, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	at, _ := time.Parse(time.DateOnly, day)
	if _, err := WriteVotes(dir, in, at); err != nil {
		t.Fatal(err)
	}
}

// Popular is the most votes gained in 30 days; trending, in 7, those
// gaining fast for their size first. Episodes, adult titles and a handful
// of votes are left out.
func TestRank(t *testing.T) {
	const trendBasics = "tconst\ttitleType\tprimaryTitle\toriginalTitle\tisAdult\tstartYear\tendYear\truntimeMinutes\tgenres\n" +
		"tt0000010\tmovie\tOld hit\tOld hit\t0\t1999\t\\N\t120\tDrama\n" +
		"tt0000011\tmovie\tNew\tNew\t0\t2026\t\\N\t100\tDrama\n" +
		"tt0000012\tmovie\tQuiet\tQuiet\t0\t2020\t\\N\t90\tDrama\n" +
		"tt0000013\tmovie\tAdult\tAdult\t1\t2026\t\\N\t90\tAdult\n" +
		"tt0000020\ttvSeries\tLong runner\tLong runner\t0\t2005\t\\N\t45\tDrama\n" +
		"tt0000021\ttvMiniSeries\tNew mini\tNew mini\t0\t2026\t2026\t50\tDrama\n" +
		"tt0000030\ttvEpisode\tBig episode\tBig episode\t0\t2026\t\\N\t50\tDrama\n"
	d, _ := build(t, trendBasics, "tconst\tparentTconst\tseasonNumber\tepisodeNumber\n", "tconst\taverageRating\tnumVotes\n")
	dir := t.TempDir()

	votesOn(t, dir, "2026-11-01", map[string]int{"tt0000010": 1_000_000, "tt0000012": 50, "tt0000013": 10, "tt0000020": 500_000, "tt0000030": 1000})
	if _, ok, err := Rank(dir, d, true); ok || err != nil {
		t.Errorf("one day: a list (%v)", err)
	}
	votesOn(t, dir, "2026-11-03", map[string]int{"tt0000010": 1_001_000, "tt0000011": 100, "tt0000012": 55, "tt0000013": 3000, "tt0000020": 500_500, "tt0000030": 5000})
	votesOn(t, dir, "2026-11-09", map[string]int{"tt0000010": 1_004_000, "tt0000011": 2600, "tt0000012": 60, "tt0000013": 5010, "tt0000020": 501_000, "tt0000021": 300, "tt0000030": 10000})

	popular, ok, err := Rank(dir, d, false)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	// Since 11-01: the old hit gained 4000, the new film 2600.
	if !slices.Equal(popular.Movies, []string{"tt0000010", "tt0000011"}) || !slices.Equal(popular.Series, []string{"tt0000020", "tt0000021"}) {
		t.Errorf("popular: %+v", popular)
	}
	// Since 11-03 (11-01 is over a week before): the new film's 2500 are
	// all its votes, the old hit's 3000 a fraction of its own.
	trending, ok, err := Rank(dir, d, true)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if !slices.Equal(trending.Movies, []string{"tt0000011", "tt0000010"}) || !slices.Equal(trending.Series, []string{"tt0000021", "tt0000020"}) {
		t.Errorf("trending: %+v", trending)
	}
	if _, ok, _ := Rank(dir, nil, true); ok {
		t.Error("without IMDb's data: a list")
	}

	// The keeper makes them once per day of votes; none of upcoming.
	k := NewKeeper(dir, nil, nil)
	if _, ok := k.Lists("trending"); ok {
		t.Error("no data in use: a list")
	}
	k.swap(d)
	if r, ok := k.Lists("trending"); !ok || !slices.Equal(r.Movies, trending.Movies) {
		t.Errorf("keeper's trending: %+v", r)
	}
	if r, ok := k.Lists("popular"); !ok || !slices.Equal(r.Movies, popular.Movies) {
		t.Errorf("keeper's popular: %+v", r)
	}
	if _, ok := k.Lists("upcoming"); ok {
		t.Error("upcoming made of votes")
	}
	votesOn(t, dir, "2026-11-10", map[string]int{"tt0000010": 1_004_000, "tt0000011": 2600, "tt0000020": 501_000, "tt0000021": 900})
	if r, _ := k.Lists("trending"); !slices.Equal(r.Series, []string{"tt0000021", "tt0000020"}) || k.rankedDay != "2026-11-10" {
		t.Errorf("a new day, made again: %+v (%s)", r, k.rankedDay)
	}
}

// Nothing gained enough: no list, not an empty one in TMDB's place.
func TestRankNothingGained(t *testing.T) {
	const basics = "tconst\ttitleType\tprimaryTitle\toriginalTitle\tisAdult\tstartYear\tendYear\truntimeMinutes\tgenres\n" +
		"tt0000010\tmovie\tA film\tA film\t0\t1999\t\\N\t120\tDrama\n"
	d, _ := build(t, basics, "tconst\tparentTconst\tseasonNumber\tepisodeNumber\n", "tconst\taverageRating\tnumVotes\n")
	dir := t.TempDir()
	votesOn(t, dir, "2026-11-01", map[string]int{"tt0000010": 1000})
	votesOn(t, dir, "2026-11-02", map[string]int{"tt0000010": 1000})
	if r, ok, err := Rank(dir, d, false); ok || err != nil {
		t.Errorf("same votes two days running: %+v, %v, %v", r, ok, err)
	}
}
