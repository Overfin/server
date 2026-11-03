package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A series of two seasons whose files are indexed by numbers, as a catalog
// read leaves them, with a mark and a position on them.
func episodeKeysFixture(t *testing.T) (*Store, *Profile, func(int) string) {
	t.Helper()
	s := openTest(t)
	ctx := context.Background()
	_, prof, err := s.CreateAccount(ctx, "subject-1", owner("1"))
	if err != nil {
		t.Fatal(err)
	}
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	n := func(i int) *int { return &i }
	ep := func(i, season, e int) CatalogItem {
		return CatalogItem{ID: id(i), Kind: "episode", Name: "E", SortName: "e", SeriesID: id(0), SeasonNumber: n(season), EpisodeNumber: n(e)}
	}
	if err := s.PutCatalog(ctx, []CatalogItem{{ID: id(0), Kind: "series", Name: "S", SortName: "s", TMDB: "7"},
		ep(1, 1, 1), ep(2, 1, 2), ep(3, 2, 1), ep(4, 9, 1)}, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshTitleKeys(ctx); err != nil {
		t.Fatal(err)
	}
	return s, prof, id
}

func keyOfItem(t *testing.T, s *Store, id string) string {
	t.Helper()
	keys, err := s.TitleKeys(context.Background(), []string{id})
	if err != nil {
		t.Fatal(err)
	}
	return keys[id]
}

func season(n int, ids ...int) Season {
	se := Season{Number: n, Name: "N"}
	for i, id := range ids {
		se.Episodes = append(se.Episodes, Episode{ID: id, Number: i + 1, Name: "E"})
	}
	return se
}

func personal(t *testing.T, s *Store, profile, key string) Personal {
	t.Helper()
	states, err := s.PersonalStates(context.Background(), profile, []string{key})
	if err != nil {
		t.Fatal(err)
	}
	return states[key]
}

func TestSeasonsTurnNumberKeysIntoIDs(t *testing.T) {
	s, prof, id := episodeKeysFixture(t)
	ctx := context.Background()
	if got := keyOfItem(t, s, id(1)); got != "series/tmdb/7/s1/e1" {
		t.Fatalf("before the season is known: %s", got)
	}
	now := time.Now()
	s.RecordPlay(ctx, prof.ID, "series/tmdb/7/s1/e1", id(1), 0, true, now)
	s.RecordPlay(ctx, prof.ID, "series/tmdb/7/s2/e1", id(3), 5000, false, now)
	// One watched under its number, and under its ID: they add up.
	s.RecordPlay(ctx, prof.ID, "series/tmdb/7/e101", id(1), 900, false, now.Add(-time.Hour))

	if err := s.SetSeason(ctx, "7", season(1, 101, 102), now); err != nil {
		t.Fatal(err)
	}
	if got := keyOfItem(t, s, id(1)); got != "series/tmdb/7/e101" {
		t.Errorf("a file of a season now known: %s", got)
	}
	if got := keyOfItem(t, s, id(3)); got != "series/tmdb/7/s2/e1" {
		t.Errorf("a file of a season still unknown: %s", got)
	}
	if st := personal(t, s, prof.ID, "series/tmdb/7/e101"); !st.Played || st.PositionMs != 900 {
		t.Errorf("merged: %+v", st)
	}
	if st := personal(t, s, prof.ID, "series/tmdb/7/s1/e1"); st.Title != "" {
		t.Errorf("the number's mark stayed: %+v", st)
	}
	if st := personal(t, s, prof.ID, "series/tmdb/7/s2/e1"); st.PositionMs != 5000 {
		t.Errorf("a season still unknown: %+v", st)
	}
	// A full pass keeps them.
	if err := s.RefreshTitleKeys(ctx); err != nil {
		t.Fatal(err)
	}
	if got := keyOfItem(t, s, id(1)); got != "series/tmdb/7/e101" {
		t.Errorf("after a pass: %s", got)
	}
	if got := keyOfItem(t, s, id(4)); got != "series/tmdb/7/s9/e1" {
		t.Errorf("a file past TMDB's end: %s", got)
	}
}

func TestEpisodeTitleByEitherKey(t *testing.T) {
	s, _, id := episodeKeysFixture(t)
	ctx := context.Background()
	if err := s.SetSeason(ctx, "7", season(1, 101, 102), time.Now()); err != nil {
		t.Fatal(err)
	}
	infos, err := s.TitleInfos(ctx, []string{"series/tmdb/7/s1/e2", "series/tmdb/7/e101", "series/tmdb/7/e999"}, "2030-01-01", "")
	if err != nil {
		t.Fatal(err)
	}
	old := infos["series/tmdb/7/s1/e2"]
	if old == nil || old.Ref.Key() != "series/tmdb/7/e102" || old.Ref.Season != 1 || old.Ref.Episode != 2 || old.Items[0] != id(2) {
		t.Errorf("an old link: %+v", old)
	}
	byID := infos["series/tmdb/7/e101"]
	if byID == nil || byID.Ref.Season != 1 || byID.Ref.Episode != 1 || byID.Items[0] != id(1) || !byID.Known {
		t.Errorf("by ID: %+v", byID)
	}
	if gone := infos["series/tmdb/7/e999"]; gone == nil || gone.Known {
		t.Errorf("an ID TMDB dropped: %+v", gone)
	}
	series := TitleRef{Kind: "series", Source: "tmdb", ID: "7"}
	if ns, err := s.SeasonEpisodes(ctx, TitleRef{Kind: "season", Source: "tmdb", ID: "7", Season: 1}); err != nil || len(ns) != 2 {
		t.Errorf("season's episodes: %v %v", ns, err)
	}
	if ns, err := s.SeriesSeasons(ctx, series, nil); err != nil || len(ns) != 3 {
		t.Errorf("series' seasons: %v %v", ns, err)
	}
	counted, err := s.TitleInfos(ctx, []string{series.Key(), "series/tmdb/7/s1"}, "2030-01-01", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := counted[series.Key()].Have; got != 4 {
		t.Errorf("series has %d", got)
	}
	if got := counted["series/tmdb/7/s1"].Have; got != 2 {
		t.Errorf("season has %d", got)
	}
}

func TestNextEpisodesByID(t *testing.T) {
	s, prof, id := episodeKeysFixture(t)
	ctx := context.Background()
	now := time.Now()
	for _, se := range []Season{season(1, 101, 102), season(2, 201)} {
		if err := s.SetSeason(ctx, "7", se, now); err != nil {
			t.Fatal(err)
		}
	}
	series := TitleRef{Kind: "series", Source: "tmdb", ID: "7"}
	// Two finished at once: the later in the order counts, though IDs say
	// nothing of order.
	s.RecordPlay(ctx, prof.ID, "series/tmdb/7/e102", id(2), 0, true, now)
	s.RecordPlay(ctx, prof.ID, "series/tmdb/7/e101", id(1), 0, true, now)
	key, _, err := s.SeriesUpNext(ctx, prof.ID, series, nil)
	if err != nil || key != "series/tmdb/7/e201" {
		t.Errorf("up next: %q %v", key, err)
	}
	titles, err := s.ContinueTitles(ctx, prof.ID, "2030-01-01", 10)
	if err != nil || len(titles) != 1 {
		t.Fatalf("continue: %v %v", titles, err)
	}
	infos, err := s.TitleInfos(ctx, titles, "2030-01-01", "")
	if err != nil || infos[titles[0]].Ref.Key() != "series/tmdb/7/e201" {
		t.Errorf("continue is %v (%v)", titles, err)
	}
	// Already watched: the one after it, past TMDB's end.
	s.RecordPlay(ctx, prof.ID, "series/tmdb/7/e201", id(3), 0, true, now.Add(-time.Hour))
	titles, err = s.ContinueTitles(ctx, prof.ID, "2030-01-01", 10)
	if err != nil || len(titles) != 1 || titles[0] != "series/tmdb/7/s9/e1" {
		t.Errorf("skips the watched: %v %v", titles, err)
	}
}

// An order says what the files are: whoever watched a file still has it, as
// the episode it turns out to be; choosing back gives the marks back.
func TestChoosingAnOrderKeepsMarksOnFiles(t *testing.T) {
	s, prof, id := episodeKeysFixture(t)
	ctx := context.Background()
	now := time.Now()
	own := []Season{season(1, 101, 102), season(2, 201)}
	for _, se := range own {
		if err := s.SetSeason(ctx, "7", se, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutTitles(ctx, []Title{{Kind: "series", TMDB: "7", Name: "S"}}, now); err != nil {
		t.Fatal(err)
	}
	s.RecordPlay(ctx, prof.ID, keyOfItem(t, s, id(1)), id(1), 0, true, now) // file S1E1, taken for 101

	other := []Season{season(1, 201, 101), season(2, 102)}
	if err := s.ArrangeSeries(ctx, "7", "abc", "Production", other, now); err != nil {
		t.Fatal(err)
	}
	if got := keyOfItem(t, s, id(1)); got != "series/tmdb/7/e201" {
		t.Errorf("file S1E1 is now %s", got)
	}
	if st := personal(t, s, prof.ID, "series/tmdb/7/e201"); !st.Played {
		t.Errorf("the mark didn't follow the file: %+v", st)
	}
	if st := personal(t, s, prof.ID, "series/tmdb/7/e101"); st.Played {
		t.Errorf("the mark stayed on the episode: %+v", st)
	}
	if g, name, err := s.SeriesOrder(ctx, "7"); g != "abc" || name != "Production" || err != nil {
		t.Errorf("order: %q %q %v", g, name, err)
	}
	if err := s.ArrangeSeries(ctx, "7", "", "", own, now); err != nil {
		t.Fatal(err)
	}
	if st := personal(t, s, prof.ID, "series/tmdb/7/e101"); !st.Played {
		t.Errorf("back on TMDB's own: %+v", st)
	}
	if g, _, err := s.SeriesOrder(ctx, "7"); g != "" || err != nil {
		t.Errorf("order: %q %v", g, err)
	}
}

// TMDB renumbering its own order moves nothing: the mark stays on the
// episode, wherever it now is.
func TestRenumberingKeepsMarksOnEpisodes(t *testing.T) {
	s, prof, id := episodeKeysFixture(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.SetSeason(ctx, "7", season(1, 101, 102), now); err != nil {
		t.Fatal(err)
	}
	s.RecordPlay(ctx, prof.ID, "series/tmdb/7/e101", id(1), 0, true, now)
	if err := s.SetSeason(ctx, "7", season(1, 102, 101), now); err != nil {
		t.Fatal(err)
	}
	if st := personal(t, s, prof.ID, "series/tmdb/7/e101"); !st.Played {
		t.Errorf("%+v", st)
	}
	r, err := s.PlaceTitle(ctx, TitleRef{Kind: "episode", Source: "tmdb", ID: "7", EpisodeID: 101})
	if err != nil || r.Season != 1 || r.Episode != 2 {
		t.Errorf("%+v %v", r, err)
	}
}

// A series identified as another: the marks go by the files' numbers, then
// by the new title's IDs.
func TestRetitleMovesEpisodeMarks(t *testing.T) {
	s, prof, id := episodeKeysFixture(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.SetSeason(ctx, "7", season(1, 101, 102), now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSeason(ctx, "8", season(1, 801, 802), now); err != nil {
		t.Fatal(err)
	}
	s.RecordPlay(ctx, prof.ID, "series/tmdb/7/e102", id(2), 0, true, now)
	if err := s.SetOverride(ctx, id(0), "8", now); err != nil {
		t.Fatal(err)
	}
	if got := keyOfItem(t, s, id(2)); got != "series/tmdb/8/e802" {
		t.Errorf("file: %s", got)
	}
	if st := personal(t, s, prof.ID, "series/tmdb/8/e802"); !st.Played {
		t.Errorf("%+v", st)
	}
}

// What's imported, by numbers, goes under the IDs.
func TestImportedMarksTakeIDs(t *testing.T) {
	s, prof, _ := episodeKeysFixture(t)
	ctx := context.Background()
	if err := s.SetSeason(ctx, "7", season(1, 101, 102), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.MergePersonal(ctx, prof.ID, []Personal{{Title: "series/tmdb/7/s1/e2", Played: true}}); err != nil {
		t.Fatal(err)
	}
	if st := personal(t, s, prof.ID, "series/tmdb/7/e102"); !st.Played {
		t.Errorf("%+v", st)
	}
}

// The valuation places episodes by ID in their seasons.
func TestValuationFactsPlaceEpisodes(t *testing.T) {
	s, prof, id := episodeKeysFixture(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.SetSeason(ctx, "7", season(1, 101, 102), now); err != nil {
		t.Fatal(err)
	}
	s.RecordPlay(ctx, prof.ID, "series/tmdb/7/e102", id(2), 0, true, now)
	f, err := s.ValuationFacts(ctx, now, nil, 90)
	if err != nil {
		t.Fatal(err)
	}
	if f.Titles["series/tmdb/7/s1"] == nil {
		t.Errorf("no season: %v", f.Titles)
	}
	if r, ok := f.Ref("series/tmdb/7/e102"); !ok || r.SeasonOf().Key() != "series/tmdb/7/s1" || r.Episode != 2 {
		t.Errorf("%+v %v", r, ok)
	}
	if _, ok := f.Ref("series/tmdb/7/e999"); ok {
		t.Error("an unknown ID was placed")
	}
}
