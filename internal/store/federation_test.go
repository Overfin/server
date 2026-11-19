package store

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/msfp-io/msfp-go"
)

// What we offer and want is per peer, replaced whole, and goes with it.
func TestPeerSharing(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	p, _ := s.AddPeer(ctx, bytes.Repeat([]byte{3}, 32), "https://a.example.org", "A", nil)
	if err := s.SetPeerOffers(ctx, p.ID, []string{"lib2", "lib1", "lib1"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.PeerOffers(ctx, p.ID); len(got) != 2 || got[0] != "lib1" {
		t.Fatalf("offers: %v", got)
	}
	s.SetPeerOffers(ctx, p.ID, []string{"lib3"})
	if got, _ := s.PeerOffers(ctx, p.ID); len(got) != 1 || got[0] != "lib3" {
		t.Fatalf("offers replaced: %v", got)
	}
	if err := s.SetPeerWants(ctx, p.ID, []PeerLibrary{{ID: "x", Name: "Films", Kind: "movies"}}); err != nil {
		t.Fatal(err)
	}
	wanted := func() []string {
		var out []string
		rows, _ := s.read.QueryContext(ctx, `SELECT name FROM peer_wants WHERE peer_id = ?`, p.ID)
		for rows.Next() {
			var n string
			rows.Scan(&n)
			out = append(out, n)
		}
		rows.Close()
		return out
	}
	if got := wanted(); len(got) != 1 || got[0] != "Films" {
		t.Fatalf("wants: %v", got)
	}
	// Unticked, then ticked again: its menu entry has the same ID (the
	// categories that name it still do).
	menuID := func() string {
		var id string
		s.db.QueryRowContext(ctx, `SELECT menu_id FROM peer_wants WHERE peer_id = ? AND library_id = 'x'`, p.ID).Scan(&id)
		return id
	}
	first := menuID()
	s.SetPeerWants(ctx, p.ID, nil)
	s.SetPeerWants(ctx, p.ID, []PeerLibrary{{ID: "x", Name: "Films", Kind: "movies"}})
	if again := menuID(); first == "" || again != first {
		t.Errorf("menu ID wanted again: %q, was %q", again, first)
	}
	s.DeletePeer(ctx, p.ID)
	if got, _ := s.PeerOffers(ctx, p.ID); len(got) != 0 {
		t.Fatalf("offers outlived the peer: %v", got)
	}
	if got := wanted(); len(got) != 0 {
		t.Fatalf("wants outlived the peer: %v", got)
	}
}

// A library's identified titles, each once, in pages; a peer's titles
// replaced whole and counted.
func TestPeerTitles(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	s.PutCatalog(ctx, []CatalogItem{
		{ID: "00000000000000000000000000000001", Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "949", LibraryID: "films",
			AudioTags: []string{"en"}},
		{ID: "00000000000000000000000000000002", Kind: "movie", Name: "Heat (4K)", SortName: "heat", TMDB: "949", LibraryID: "films4k",
			AudioTags: []string{"en", "fr-CA"}, SubtitleTags: []string{"fr"}},
		{ID: "00000000000000000000000000000003", Kind: "movie", Name: "Home", SortName: "home", LibraryID: "films"},
		{ID: "00000000000000000000000000000004", Kind: "movie", Name: "Alien", SortName: "alien", TMDB: "348", LibraryID: "other"},
		{ID: "00000000000000000000000000000005", Kind: "movie", Name: "Up", SortName: "up", TMDB: "14160", LibraryID: "films"},
	}, 1)
	if err := s.RefreshTitleKeys(ctx); err != nil {
		t.Fatal(err)
	}
	cat := MSFPCatalog{S: s}
	page, err := cat.Titles(ctx, []string{"films", "films4k"}, "", 1)
	if err != nil || len(page.Titles) != 1 || page.Titles[0].Common().Key != "movie/tmdb/14160" || !page.More ||
		!slices.Equal(page.Titles[0].Common().Libraries, []string{"films"}) {
		t.Fatalf("first page: %+v, %v", page, err)
	}
	page, _ = cat.Titles(ctx, []string{"films", "films4k"}, "movie/tmdb/14160", 10)
	// Heat is in both: both libraries given, the engine picks.
	if len(page.Titles) != 1 || page.Titles[0].Common().Key != "movie/tmdb/949" ||
		!slices.Equal(page.Titles[0].Common().Libraries, []string{"films", "films4k"}) || page.More {
		t.Fatalf("second page (Heat once, the unidentified file and the other library left out): %+v", page)
	}
	// Its languages: both copies'.
	heat := page.Titles[0].(*msfp.VideoTitle)
	if a, s := heat.Audio, heat.Subtitles; len(a) != 2 || a[0] != "en" || a[1] != "fr-CA" || len(s) != 1 || s[0] != "fr" {
		t.Fatalf("languages: %v %v", a, s)
	}
	if only, _ := cat.Titles(ctx, []string{"films"}, "movie/tmdb/14160", 10); len(only.Titles[0].(*msfp.VideoTitle).Audio) != 1 {
		t.Fatalf("languages from a library not asked for: %+v", only)
	}
	if got, _ := cat.Titles(ctx, nil, "", 10); len(got.Titles) != 0 {
		t.Fatalf("no library: %+v", got)
	}
	p, _ := s.AddPeer(ctx, bytes.Repeat([]byte{3}, 32), "https://a.example.org", "A", nil)
	if err := s.SetPeerTitles(ctx, p.ID, []PeerTitle{{Key: "movie/tmdb/1", Kind: "movie", Library: "x", Audio: []string{"fr-CA"}}, {Key: "series/tmdb/2", Kind: "series", Library: "x"}, {Key: "series/tmdb/2/s1", Kind: "season", Library: "x"}}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.PeerTitleCounts(ctx); n[p.ID] != 2 {
		t.Fatalf("counts: %v", n)
	}
	s.SetPeerTitles(ctx, p.ID, nil)
	if n, _ := s.PeerTitleCounts(ctx); n[p.ID] != 0 {
		t.Fatalf("not replaced: %v", n)
	}

	// Page by page: a reading cut short keeps what was known, with what it
	// added; a whole one leaves out what it didn't see.
	films := func(from, to int) []PeerTitle {
		var out []PeerTitle
		for i := from; i < to; i++ {
			out = append(out, PeerTitle{Key: fmt.Sprintf("movie/tmdb/%d", i), Kind: "movie", Library: "x"})
		}
		return out
	}
	first := PeerReading()
	s.PutPeerTitles(ctx, p.ID, first, films(1, 6))
	s.PutPeerTitles(ctx, p.ID, first, films(6, 11))
	s.EndPeerTitles(ctx, p.ID, first)
	cut := PeerReading()
	s.PutPeerTitles(ctx, p.ID, cut, films(20, 22))
	if n, _ := s.PeerTitleCounts(ctx); n[p.ID] != 12 {
		t.Fatalf("cut short: %v", n)
	}
	whole := PeerReading()
	s.PutPeerTitles(ctx, p.ID, whole, films(3, 5))
	s.PutPeerTitles(ctx, p.ID, whole, films(21, 22))
	s.EndPeerTitles(ctx, p.ID, whole)
	if n, _ := s.PeerTitleCounts(ctx); n[p.ID] != 3 {
		t.Fatalf("whole: %v", n)
	}
	// An earlier reading writing late never makes a title look unseen by
	// a later one.
	earlier, later := PeerReading(), PeerReading()
	s.PutPeerTitles(ctx, p.ID, later, films(3, 5))
	s.PutPeerTitles(ctx, p.ID, earlier, films(3, 5))
	s.EndPeerTitles(ctx, p.ID, later)
	if n, _ := s.PeerTitleCounts(ctx); n[p.ID] != 2 {
		t.Fatalf("an earlier reading late: %v", n)
	}
}

// TMDB's data is kept for what paired servers hold too: a series with the
// seasons they have; nothing of a server not paired.
func TestHeldTitles(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	p, _ := s.AddPeer(ctx, bytes.Repeat([]byte{3}, 32), "https://a.example.org", "A", nil)
	s.SetPeerTitles(ctx, p.ID, []PeerTitle{{Key: "series/tmdb/1399", Kind: "series", Library: "x"},
		{Key: "series/tmdb/1399/s1", Kind: "season", Library: "x"}, {Key: "series/tmdb/1399/s3", Kind: "season", Library: "x"},
		{Key: "movie/tmdb/949", Kind: "movie", Library: "x"}, {Key: "series/tvdb/5", Kind: "series", Library: "x"}})
	if got, _ := s.ArtworkToFetch(ctx, time.Now(), 10); len(got) != 0 {
		t.Fatalf("a server not paired yet: %+v", got)
	}
	s.SetPeerState(ctx, p.ID, PeerTheirs, PeerPaired)
	got, err := s.ArtworkToFetch(ctx, time.Now(), 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("artwork: %+v, %v", got, err)
	}
	for _, a := range got {
		if a.Kind == "series" && (a.TMDB != "1399" || len(a.Seasons) != 2 || a.Seasons[0]+a.Seasons[1] != 4) {
			t.Fatalf("series: %+v", a)
		}
		if a.Kind == "movie" && a.TMDB != "949" {
			t.Fatalf("film: %+v", a)
		}
	}
	if got, _ := s.TextsToFetch(ctx, "fr", time.Now(), 10); len(got) != 2 {
		t.Fatalf("texts: %+v", got)
	}
	if got, _ := s.TitlesToRate(ctx, time.Now(), time.Now(), "2026-11-10", 10); len(got) != 2 {
		t.Fatalf("ratings: %+v", got)
	}
}

// What a paired server holds for us counts as held: its copies are Peers,
// its episodes count in its series' and seasons' (once each, with ours),
// a specials season's in its own only; a server not paired counts none.
func TestHeldByPeers(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	one := 1
	s.PutCatalog(ctx, []CatalogItem{
		{ID: "00000000000000000000000000000001", Kind: "series", Name: "S", SortName: "s", TMDB: "2", LibraryID: "tv"},
		{ID: "00000000000000000000000000000002", Kind: "episode", Name: "E1", SortName: "e1", LibraryID: "tv",
			SeriesID: "00000000000000000000000000000001", SeasonNumber: &one, EpisodeNumber: &one},
	}, 1)
	if err := s.RefreshTitleKeys(ctx); err != nil {
		t.Fatal(err)
	}
	p, _ := s.AddPeer(ctx, bytes.Repeat([]byte{3}, 32), "https://a.example.org", "A", nil)
	s.SetPeerTitles(ctx, p.ID, []PeerTitle{{Key: "movie/tmdb/1", Kind: "movie", Library: "x"},
		{Key: "series/tmdb/2", Kind: "series", Library: "x"}, {Key: "series/tmdb/2/s1", Kind: "season", Library: "x"},
		{Key: "series/tmdb/2/s1/e1", Kind: "episode", Library: "x"}, {Key: "series/tmdb/2/s1/e2", Kind: "episode", Library: "x"},
		{Key: "series/tmdb/2/s0/e1", Kind: "episode", Library: "x"}, {Key: "series/tmdb/2/e777", Kind: "episode", Library: "x"}})
	keys := []string{"movie/tmdb/1", "series/tmdb/2", "series/tmdb/2/s1", "series/tmdb/2/s0"}
	read := func() map[string]*TitleInfo {
		t.Helper()
		got, err := s.TitleInfos(ctx, keys, "2030-01-01", "")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := read(); len(got["movie/tmdb/1"].Peers) != 0 || got["series/tmdb/2"].Have != 1 {
		t.Fatalf("a peer not paired yet: %+v %+v", got["movie/tmdb/1"], got["series/tmdb/2"])
	}
	s.SetPeerState(ctx, p.ID, PeerTheirs, PeerPaired)
	got := read()
	if f := got["movie/tmdb/1"]; len(f.Peers) != 1 || f.Peers[0] != p.ID || len(f.Items) != 0 {
		t.Fatalf("film: %+v", f)
	}
	// The series: e1 (both), e2, and the one by its ID; not the special.
	if se := got["series/tmdb/2"]; se.Have != 3 || len(se.Peers) != 1 {
		t.Fatalf("series: have %d, peers %v", se.Have, se.Peers)
	}
	if got["series/tmdb/2/s1"].Have != 2 || got["series/tmdb/2/s0"].Have != 1 {
		t.Fatalf("seasons: %d, specials %d", got["series/tmdb/2/s1"].Have, got["series/tmdb/2/s0"].Have)
	}
	s1, _ := ParseTitleKey("series/tmdb/2/s1")
	if eps, err := s.SeasonEpisodes(ctx, s1); err != nil || !slices.Equal(eps, []int{1, 2}) {
		t.Fatalf("season 1's episodes: %v %v", eps, err)
	}
	if seasons, err := s.SeriesSeasons(ctx, s1.Series(), nil); err != nil || !slices.Equal(seasons, []int{0, 1}) {
		t.Fatalf("seasons: %v %v", seasons, err)
	}
	if m, _ := s.TitlePeers(ctx, keys); len(m["movie/tmdb/1"]) != 1 || len(m["series/tmdb/2/s0"]) != 0 {
		t.Fatalf("TitlePeers: %v", m)
	}
}

// Listings read paired servers' films and series too: not for a profile
// limited to some libraries, nor one a tag of theirs is hidden from; an
// entry keeps its ID while wanted, and a title its arrival while held.
func TestPeerListing(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	now := time.Unix(1_000_000, 0)
	s.now = func() time.Time { return now }
	p, _ := s.AddPeer(ctx, bytes.Repeat([]byte{3}, 32), "https://a.example.org", "A", nil)
	s.SetPeerState(ctx, p.ID, PeerTheirs, PeerPaired)
	s.SetPeerWants(ctx, p.ID, []PeerLibrary{{ID: "x", Name: "Films", Kind: "movies"}})
	menu, _ := s.MenuLibraries(ctx)
	if len(menu) != 0 {
		t.Fatalf("an entry holding nothing: %+v", menu)
	}
	s.SetPeerTitles(ctx, p.ID, []PeerTitle{{Key: "movie/tmdb/1", Kind: "movie", Library: "x"}, {Key: "movie/tmdb/2", Kind: "movie", Library: "x"},
		{Key: "movie/tmdb/3", Kind: "movie", Library: "x"}})
	s.SetTitleDetails(ctx, Title{Kind: "movie", TMDB: "1", Name: "One"}, []byte(`{}`), now)
	s.SetTitleDetails(ctx, Title{Kind: "movie", TMDB: "2", Name: "Two", Keywords: []string{"Horror"}}, []byte(`{}`), now)
	// 3: TMDB's page of it isn't kept, so it isn't listed.
	menu, _ = s.MenuLibraries(ctx)
	if len(menu) != 1 || menu[0].Name != "Films (A)" || len(menu[0].ID) != 32 {
		t.Fatalf("menu: %+v", menu)
	}
	list := func(q ListQuery) []string {
		t.Helper()
		q.Sort, q.Limit = "name", 10
		ids, err := s.ListCatalog(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		keys, _ := s.TitleKeys(ctx, ids)
		var out []string
		for _, id := range ids {
			out = append(out, keys[id])
		}
		if n, _ := s.CountCatalog(ctx, q); n != len(out) {
			t.Fatalf("count %d for %v", n, out)
		}
		return out
	}
	if got := list(ListQuery{LibraryID: menu[0].ID}); len(got) != 2 || got[0] != "movie/tmdb/1" {
		t.Fatalf("the entry: %v", got)
	}
	if got := list(ListQuery{Access: &Access{BlockedTags: []string{"horror"}}}); len(got) != 1 || got[0] != "movie/tmdb/1" {
		t.Fatalf("horror hidden: %v", got)
	}
	if got := list(ListQuery{Access: &Access{Libraries: []string{menu[0].ID}}}); len(got) != 0 {
		t.Fatalf("limited to some libraries: %v", got)
	}
	if got := list(ListQuery{Kinds: []string{"episode"}}); len(got) != 0 {
		t.Fatalf("episodes: %v", got)
	}
	if got, err := s.SearchLibrary(ctx, "one", 10); err != nil || len(got) != 1 || got[0] != "movie/tmdb/1" {
		t.Fatalf("search: %v %v", got, err)
	}
	// Read again later: the entry and the arrival are kept.
	now = now.Add(time.Hour)
	s.SetPeerWants(ctx, p.ID, []PeerLibrary{{ID: "x", Name: "Movies", Kind: "movies"}, {ID: "y", Name: "Series", Kind: "tvshows"}})
	s.SetPeerTitles(ctx, p.ID, []PeerTitle{{Key: "movie/tmdb/1", Kind: "movie", Library: "x"}, {Key: "movie/tmdb/2", Kind: "movie", Library: "x"}})
	again, _ := s.MenuLibraries(ctx)
	if len(again) != 1 || again[0].ID != menu[0].ID || again[0].Name != "Movies (A)" {
		t.Fatalf("menu again: %+v (was %+v)", again, menu)
	}
	var added int64
	s.read.QueryRow(`SELECT added_at FROM peer_titles WHERE title_key = 'movie/tmdb/1'`).Scan(&added)
	if added != 1_000_000 {
		t.Fatalf("arrival: %d", added)
	}
	if got := list(ListQuery{Sort: "latest"}); len(got) != 2 {
		t.Fatalf("latest: %v", got)
	}
	s.LabelPeerWant(ctx, p.ID, "x", "Theirs")
	if again, _ := s.MenuLibraries(ctx); again[0].Name != "Theirs" {
		t.Fatalf("labelled: %+v", again)
	}
	if err := s.LabelPeerWant(ctx, p.ID, "nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("labelling what isn't wanted: %v", err)
	}
	// A library no longer wanted: gone from the menu and the listings, its
	// titles with it at once.
	s.SetPeerWants(ctx, p.ID, nil)
	if got := list(ListQuery{}); len(got) != 0 {
		t.Fatalf("after: %v", got)
	}
	var left int
	s.read.QueryRow(`SELECT count(*) FROM peer_titles WHERE peer_id = ?`, p.ID).Scan(&left)
	if left != 0 {
		t.Fatalf("%d titles left of a library no longer wanted", left)
	}
}

// What a paired server is told of each title (MSFP video, sections 3 and
// 4): TMDB's keys only, never a title known by IMDb's or TheTVDB's ID; an
// episode of a file holding several, the others it holds (with); films,
// series and episodes, their other IDs as TMDB gives them. And the copy
// of an episode a file holds after its first is that file's item.
func TestPeerTitlesVideoMembers(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	id := func(n int) string { return fmt.Sprintf("%032x", n) }
	n := func(i int) *int { return &i }
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(1), Kind: "movie", Name: "The Matrix", SortName: "matrix", TMDB: "603", LibraryID: "lib"},
		{ID: id(2), Kind: "movie", Name: "Known by IMDb", SortName: "k", IMDb: "tt0133093", LibraryID: "lib"},
		{ID: id(3), Kind: "series", Name: "By TVDB", SortName: "b", TVDB: "81189", LibraryID: "lib"},
		{ID: id(4), Kind: "season", Name: "S1", SortName: "s1", SeriesID: id(3), SeasonNumber: n(1), LibraryID: "lib"},
		{ID: id(10), Kind: "series", Name: "GoT", SortName: "got", TMDB: "1399", LibraryID: "lib"},
		{ID: id(11), Kind: "season", Name: "S1", SortName: "s1", SeriesID: id(10), SeasonNumber: n(1), LibraryID: "lib"},
		{ID: id(12), Kind: "episode", Name: "E1-E2", SortName: "e1", SeriesID: id(10), SeasonID: id(11), SeasonNumber: n(1),
			EpisodeNumber: n(1), EpisodeNumberEnd: n(2), LibraryID: "lib", AudioTags: []string{"en"}},
		{ID: id(13), Kind: "episode", Name: "E3", SortName: "e3", SeriesID: id(10), SeasonID: id(11), SeasonNumber: n(1),
			EpisodeNumber: n(3), LibraryID: "lib"},
	}, 1)
	s.db.ExecContext(ctx, `INSERT INTO own_episodes (series, season, episode, tmdb_id) VALUES ('1399', 1, 1, 63056), ('1399', 1, 2, 63057)`)
	s.db.ExecContext(ctx, `INSERT INTO catalog_titles (kind, tmdb, name, overview, status, seen_at, imdb, tvdb)
		VALUES ('movie', '603', 'The Matrix', '', 0, 0, 'tt0133093', NULL), ('series', '1399', 'GoT', '', 0, 0, 'tt0944947', '121361')`)
	s.db.ExecContext(ctx, `INSERT INTO episode_imdb (series, tmdb_id, imdb, checked_at) VALUES ('1399', 63056, 'tt1480055', 0), ('1399', 63057, 'bad', 0)`)
	if err := s.RefreshTitleKeys(ctx); err != nil {
		t.Fatal(err)
	}
	cat := MSFPCatalog{S: s}
	page, err := cat.Titles(ctx, []string{"lib"}, "", 100)
	if err != nil || page.More {
		t.Fatal(err, page.More)
	}
	byKey := map[string]*msfp.VideoTitle{}
	var keys []string
	for _, pt := range page.Titles {
		v := pt.(*msfp.VideoTitle)
		byKey[v.Key] = v
		keys = append(keys, v.Key)
	}
	want := []string{"movie/tmdb/603", "series/tmdb/1399", "series/tmdb/1399/e63056", "series/tmdb/1399/e63057",
		"series/tmdb/1399/s1", "series/tmdb/1399/s1/e3"}
	if !slices.Equal(keys, want) {
		t.Fatalf("keys %v, want %v", keys, want)
	}
	if w := byKey["series/tmdb/1399/e63056"].With; !slices.Equal(w, []string{"series/tmdb/1399/e63057"}) {
		t.Errorf("the file's first episode: with %v", w)
	}
	if w := byKey["series/tmdb/1399/e63057"].With; !slices.Equal(w, []string{"series/tmdb/1399/e63056"}) {
		t.Errorf("the file's second episode: with %v", w)
	}
	if w := byKey["series/tmdb/1399/s1/e3"].With; w != nil {
		t.Errorf("an episode alone in its file: with %v", w)
	}
	if ids := byKey["movie/tmdb/603"].IDs; len(ids) != 1 || ids["imdb"] != "tt0133093" {
		t.Errorf("film ids %v", ids)
	}
	if ids := byKey["series/tmdb/1399"].IDs; len(ids) != 2 || ids["imdb"] != "tt0944947" || ids["tvdb"] != "121361" {
		t.Errorf("series ids %v", ids)
	}
	if ids := byKey["series/tmdb/1399/e63056"].IDs; len(ids) != 1 || ids["imdb"] != "tt1480055" {
		t.Errorf("episode ids %v", ids)
	}
	// Each episode keyed by its TMDB ID has its place in TMDB's order
	// (MSFP video section 4.1); nothing else has one.
	for key, want := range map[string]msfp.Place{"series/tmdb/1399/e63056": {Season: 1, Episode: 1}, "series/tmdb/1399/e63057": {Season: 1, Episode: 2}} {
		if p := byKey[key].Place; p == nil || *p != want {
			t.Errorf("%s place %v, want %v", key, p, want)
		}
	}
	if p := byKey["movie/tmdb/603"].Place; p != nil {
		t.Errorf("a film has a place: %v", p)
	}
	if ids := byKey["series/tmdb/1399/e63057"].IDs; ids != nil {
		t.Errorf("an IMDb ID not in its form sent: %v", ids)
	}
	// The second episode plays the file: its copy, of the library holding
	// it, named by its video.
	copies, err := cat.Copies(ctx, "series/tmdb/1399/e63057", []string{"lib"})
	if err != nil || len(copies) != 1 || copies[0].Handle != id(12) || !slices.Equal(copies[0].Audio, []string{"en"}) || copies[0].Library != "lib" {
		t.Errorf("copies %+v %v", copies, err)
	}
	if copies, _ := cat.Copies(ctx, "series/tmdb/1399/e63057", []string{"other"}); len(copies) != 0 {
		t.Errorf("copies in a library not asked for: %+v", copies)
	}
	// A page looked at whole is full, whatever was left out of it.
	if page, _ := cat.Titles(ctx, []string{"lib"}, "", len(want)); !page.More {
		t.Error("a full page not said so")
	}
}

// A paired server's with is kept with its episode, for watching them all.
func TestPeerWith(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	key, _ := hex.DecodeString(strings.Repeat("cd", 32))
	p, err := s.AddPeer(ctx, key, "https://b.example.org", "B", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPeerTitles(ctx, p.ID, []PeerTitle{{Key: "series/tmdb/1/e1", Kind: "episode", Library: "L",
		With: []string{"series/tmdb/1/e2"}}, {Key: "series/tmdb/1/e3", Kind: "episode", Library: "L"}}); err != nil {
		t.Fatal(err)
	}
	if w, err := s.PeerWith(ctx, p.ID, "series/tmdb/1/e1"); err != nil || !slices.Equal(w, []string{"series/tmdb/1/e2"}) {
		t.Errorf("%v %v", w, err)
	}
	for _, k := range []string{"series/tmdb/1/e3", "series/tmdb/1/e9"} {
		if w, err := s.PeerWith(ctx, p.ID, k); err != nil || w != nil {
			t.Errorf("%s: %v %v", k, w, err)
		}
	}
	var got []msfp.Title
	s.MSFP().Read(ctx, func(tx msfp.Tx) error {
		var err error
		got, err = tx.PeerTitles(p.ID, "", 10)
		return err
	})
	if len(got) != 2 || !slices.Equal(got[0].(*msfp.VideoTitle).With, []string{"series/tmdb/1/e2"}) {
		t.Errorf("%+v", got)
	}
}

// A change number with no title changed: above every one before, and
// never listed.
func TestCatalogBumpSeq(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	cat := MSFPCatalog{S: s}
	before, _ := cat.Seq(ctx)
	seq, err := cat.BumpSeq(ctx)
	if err != nil || seq != before+1 {
		t.Fatalf("bumped to %d from %d: %v", seq, before, err)
	}
	if now, _ := cat.Seq(ctx); now != seq {
		t.Fatalf("seq %d after a bump to %d", now, seq)
	}
	page, err := cat.Changes(ctx, []string{"lib"}, before, "", 10)
	if err != nil || len(page.Present)+len(page.Gone) != 0 {
		t.Fatalf("a bump listed: %+v %v", page, err)
	}
}

// Pictures (MSFP video section 3.2): each copy's, kept stepped, given to
// the engine with the title; copies listed best first, so that play
// without a version plays the one the title's picture describes; a new
// picture is a change of the title; a peer's kept with its title.
func TestPictures(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	id := func(n int) string { return fmt.Sprintf("%032x", n) }
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(1), Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "949", LibraryID: "lib", Height: 1080},
		{ID: id(2), Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "949", LibraryID: "lib"},
		{ID: id(3), Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "949", LibraryID: "lib", Height: 2200, HDR: []string{"dv", "hdr10"}},
		{ID: id(4), Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "949", LibraryID: "lib", Height: 2160},
	}, 1)
	if err := s.RefreshTitleKeys(ctx); err != nil {
		t.Fatal(err)
	}
	var kept string
	s.db.QueryRowContext(ctx, `SELECT picture FROM catalog_items WHERE id = ?`, id(3)).Scan(&kept)
	if kept != `{"height":2160,"hdr":["hdr10","dv"]}` {
		t.Errorf("kept: %s", kept)
	}
	cat := MSFPCatalog{S: s}
	page, err := cat.Titles(ctx, []string{"lib"}, "", 10)
	if err != nil || len(page.Titles) != 1 {
		t.Fatal(err, page)
	}
	if p := page.Titles[0].(*msfp.VideoTitle).Pictures; len(p) != 3 {
		t.Errorf("pictures: %+v", p)
	}
	copies, err := cat.Copies(ctx, "movie/tmdb/949", []string{"lib"})
	var order []string
	for _, c := range copies {
		order = append(order, c.Handle)
	}
	if err != nil || !slices.Equal(order, []string{id(3), id(4), id(1), id(2)}) {
		t.Errorf("copies: %v %v", order, err)
	}
	head, _, _ := s.TitleChanges(ctx)
	s.PutCatalog(ctx, []CatalogItem{{ID: id(1), Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "949", LibraryID: "lib", Height: 720}}, 1)
	changes, err := cat.Changes(ctx, []string{"lib"}, head, "", 10)
	if err != nil || len(changes.Present) != 1 {
		t.Errorf("a new picture: %+v %v", changes, err)
	}
}
