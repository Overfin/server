package store

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// The key is made once: a second start racing the first keeps the first.
func TestFederationSeed(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	if _, ok, err := s.FederationSeed(ctx); ok || err != nil {
		t.Fatalf("a key before any was made: %v %v", ok, err)
	}
	first := bytes.Repeat([]byte{1}, 32)
	if kept, err := s.KeepFederationSeed(ctx, first); err != nil || !bytes.Equal(kept, first) {
		t.Fatalf("kept %x, %v", kept, err)
	}
	if kept, err := s.KeepFederationSeed(ctx, bytes.Repeat([]byte{2}, 32)); err != nil || !bytes.Equal(kept, first) {
		t.Fatalf("a second key replaced the first: %x, %v", kept, err)
	}
}

// A code is redeemed once, by its secret, only if the check passes and
// before it expires; the server that used it waits for our operator.
func TestRedeemPairingCode(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	clock := time.Unix(1_800_000_000, 0)
	s.now = func() time.Time { return clock }
	secret := bytes.Repeat([]byte{7}, 32)
	_, err := s.CreatePairingCode(ctx, secret, "for Marc", clock.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if list, _ := s.PairingCodes(ctx); len(list) != 1 || list[0].Note != "for Marc" {
		t.Fatalf("codes: %+v", list)
	}
	key := bytes.Repeat([]byte{3}, 32)
	hash := PairingSecretHash(secret)
	refuse := func([]byte) bool { return false }
	if _, err := s.RedeemPairingCode(ctx, hash, key, "https://b.example.org", "B", refuse); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refused proof: %v", err)
	}
	if _, err := s.RedeemPairingCode(ctx, PairingSecretHash([]byte("other")), key, "https://b.example.org", "B",
		func([]byte) bool { return true }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another secret: %v", err)
	}
	var seen []byte
	p, err := s.RedeemPairingCode(ctx, hash, key, "https://b.example.org", "B", func(got []byte) bool { seen = got; return true })
	if err != nil || p.State != PeerOurs || !bytes.Equal(seen, secret) {
		t.Fatalf("redeemed: %+v, %v (secret %x)", p, err, seen)
	}
	if _, err := s.RedeemPairingCode(ctx, hash, bytes.Repeat([]byte{4}, 32), "https://c.example.org", "C",
		func([]byte) bool { return true }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a code used twice: %v", err)
	}
	if list, _ := s.PairingCodes(ctx); len(list) != 0 {
		t.Fatalf("a used code is still listed: %+v", list)
	}

	// An expired code.
	secret2 := bytes.Repeat([]byte{8}, 32)
	if _, err := s.CreatePairingCode(ctx, secret2, "", clock.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(2 * time.Hour)
	if _, err := s.RedeemPairingCode(ctx, PairingSecretHash(secret2), bytes.Repeat([]byte{5}, 32), "https://d.example.org", "D",
		func([]byte) bool { return true }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an expired code: %v", err)
	}

	// A key already known keeps the code.
	secret3 := bytes.Repeat([]byte{6}, 32)
	if _, err := s.CreatePairingCode(ctx, secret3, "", clock.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemPairingCode(ctx, PairingSecretHash(secret3), key, "https://b.example.org", "B",
		func([]byte) bool { return true }); !errors.Is(err, ErrPeerExists) {
		t.Fatalf("a known key: %v", err)
	}
	if list, _ := s.PairingCodes(ctx); len(list) != 1 {
		t.Fatalf("the code was used up by a known key: %+v", list)
	}
}

func TestPeers(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	key := bytes.Repeat([]byte{3}, 32)
	p, err := s.AddPeer(ctx, key, "https://a.example.org", "A")
	if err != nil || p.State != PeerTheirs {
		t.Fatalf("added: %+v, %v", p, err)
	}
	if _, err := s.AddPeer(ctx, key, "https://a.example.org", "A"); !errors.Is(err, ErrPeerExists) {
		t.Fatalf("added twice: %v", err)
	}
	if ok, _ := s.SetPeerState(ctx, p.ID, PeerOurs, PeerPaired); ok {
		t.Fatal("moved from a state it wasn't in")
	}
	if ok, err := s.SetPeerState(ctx, p.ID, PeerTheirs, PeerPaired); !ok || err != nil {
		t.Fatalf("pairing: %v %v", ok, err)
	}
	got, err := s.PeerByKey(ctx, key)
	if err != nil || got.State != PeerPaired || got.Paired == nil {
		t.Fatalf("by key: %+v, %v", got, err)
	}
	if list, _ := s.Peers(ctx); len(list) != 1 {
		t.Fatalf("peers: %+v", list)
	}
	if err := s.DeletePeer(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PeerByID(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("still there: %v", err)
	}
	if err := s.DeletePeer(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted twice: %v", err)
	}
}

// What we offer and want is per peer, replaced whole, and goes with it.
func TestPeerSharing(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	p, _ := s.AddPeer(ctx, bytes.Repeat([]byte{3}, 32), "https://a.example.org", "A")
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
	if got, _ := s.PeerWants(ctx, p.ID); len(got) != 1 || got[0].Name != "Films" {
		t.Fatalf("wants: %v", got)
	}
	s.DeletePeer(ctx, p.ID)
	if got, _ := s.PeerOffers(ctx, p.ID); len(got) != 0 {
		t.Fatalf("offers outlived the peer: %v", got)
	}
	if got, _ := s.PeerWants(ctx, p.ID); len(got) != 0 {
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
	got, err := s.TitlesInLibraries(ctx, []string{"films", "films4k"}, "", 1)
	if err != nil || len(got) != 1 || got[0].Key != "movie/tmdb/14160" {
		t.Fatalf("first page: %+v, %v", got, err)
	}
	got, _ = s.TitlesInLibraries(ctx, []string{"films", "films4k"}, got[0].Key, 10)
	if len(got) != 1 || got[0].Key != "movie/tmdb/949" || got[0].Library != "films" {
		t.Fatalf("second page (Heat once, the unidentified file and the other library left out): %+v", got)
	}
	// Its languages: both copies'.
	if a, s := got[0].Audio, got[0].Subtitles; len(a) != 2 || a[0] != "en" || a[1] != "fr-CA" || len(s) != 1 || s[0] != "fr" {
		t.Fatalf("languages: %v %v", a, s)
	}
	if only, _ := s.TitlesInLibraries(ctx, []string{"films"}, "movie/tmdb/14160", 10); len(only[0].Audio) != 1 {
		t.Fatalf("languages from a library not asked for: %+v", only)
	}
	if got, _ := s.TitlesInLibraries(ctx, nil, "", 10); len(got) != 0 {
		t.Fatalf("no library: %+v", got)
	}
	p, _ := s.AddPeer(ctx, bytes.Repeat([]byte{3}, 32), "https://a.example.org", "A")
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
}

// TMDB's data is kept for what paired servers hold too: a series with the
// seasons they have; nothing of a server not paired.
func TestHeldTitles(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	p, _ := s.AddPeer(ctx, bytes.Repeat([]byte{3}, 32), "https://a.example.org", "A")
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
	p, _ := s.AddPeer(ctx, bytes.Repeat([]byte{3}, 32), "https://a.example.org", "A")
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
	p, _ := s.AddPeer(ctx, bytes.Repeat([]byte{3}, 32), "https://a.example.org", "A")
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
