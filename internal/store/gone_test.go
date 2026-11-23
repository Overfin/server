package store

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/msfp-io/msfp-go"
)

// When TMDB merges a title into another, what's kept under its old ID
// moves to the new one and adds up with what the new one has; later
// writes under the old key go to the new one.
func TestMoveGone(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, p, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	at := time.Unix(1_700_000_000, 0)

	// A film: watched under the old ID, half seen and a favorite under the
	// new one (a peer's copy).
	if err := s.RecordPlay(ctx, p.ID, "movie/tmdb/100", "", 0, true, at); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPlay(ctx, p.ID, "movie/tmdb/200", "", 600_000, false, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFavorite(ctx, p.ID, "movie/tmdb/200", "", true, at); err != nil {
		t.Fatal(err)
	}
	// A series: an episode under the old ID; "series/tmdb/1000" must not move.
	if err := s.RecordPlay(ctx, p.ID, "series/tmdb/10/s1/e2", "", 0, true, at); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPlay(ctx, p.ID, "series/tmdb/1000/s1/e1", "", 0, true, at); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"movie/tmdb/100", "movie/tmdb/200", "series/tmdb/10/s2"} {
		if err := s.AddWish(ctx, p.ID, w); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetNotInterested(ctx, p.ID, "series/tmdb/10", true, at); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTitleEdit(ctx, "movie", "100", "name", "fr", "Le nom choisi", at); err != nil {
		t.Fatal(err)
	}

	if err := s.MoveGone(ctx, "movie", "100", "200", at); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveGone(ctx, "series", "10", "20", at); err != nil {
		t.Fatal(err)
	}

	got, err := s.PersonalStates(ctx, p.ID, []string{"movie/tmdb/100", "movie/tmdb/200", "series/tmdb/20/s1/e2", "series/tmdb/1000/s1/e1"})
	if err != nil {
		t.Fatal(err)
	}
	film := got["movie/tmdb/200"]
	if !film.Played || !film.Favorite || film.PositionMs != 600_000 || film.PlayCount != 1 {
		t.Fatalf("film added up: %+v", film)
	}
	if old := got["movie/tmdb/100"]; old.Title != "movie/tmdb/100" || !old.Played || !old.Favorite {
		t.Fatalf("old key asked: %+v", old)
	}
	if !got["series/tmdb/20/s1/e2"].Played || !got["series/tmdb/1000/s1/e1"].Played {
		t.Fatalf("episodes: %+v", got)
	}
	var left int
	if err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM profile_titles WHERE title = 'movie/tmdb/100' OR title LIKE 'series/tmdb/10/%'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("left under old keys: %d %v", left, err)
	}

	wishes, _ := s.Wishes(ctx, p.ID)
	if w := wishTitles(wishes); !slices.Equal(w, []string{"movie/tmdb/200", "series/tmdb/20/s2"}) {
		t.Fatalf("wishes: %v", w)
	}
	ni, _ := s.NotInterested(ctx, p.ID)
	if !ni["series/tmdb/20"] || ni["series/tmdb/10"] {
		t.Fatalf("not interested: %v", ni)
	}
	if e, _ := s.TitleEdits(ctx, "movie", "200"); len(e) != 1 || e[0].Value != "Le nom choisi" {
		t.Fatalf("edits: %+v", e)
	}

	// Written later under the old key: lands under the new one.
	if err := s.RecordPlay(ctx, p.ID, "series/tmdb/10/s1/e3", "", 1000, false, at); err != nil {
		t.Fatal(err)
	}
	if err := s.AddWish(ctx, p.ID, "movie/tmdb/100"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.PersonalStates(ctx, p.ID, []string{"series/tmdb/20/s1/e3"})
	if got["series/tmdb/20/s1/e3"].PositionMs != 1000 {
		t.Fatalf("redirected play: %+v", got)
	}
	if wishes, _ := s.Wishes(ctx, p.ID); len(wishes) != 2 {
		t.Fatalf("redirected wish: %v", wishTitles(wishes))
	}
}

// A chain of merges ends on the last ID; a title that comes back is no
// longer gone; an unknown new ID moves nothing.
func TestGoneChain(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	at := time.Unix(1_700_000_000, 0)
	if err := s.SetGone(ctx, "movie", "1", "2", at); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGone(ctx, "movie", "2", "3", at); err != nil {
		t.Fatal(err)
	}
	if k := s.CurrentKey(ctx, "movie/tmdb/1"); k != "movie/tmdb/3" {
		t.Fatalf("chain: %s", k)
	}
	if k := s.CurrentKey(ctx, "series/tmdb/1"); k != "series/tmdb/1" {
		t.Fatalf("other kind: %s", k)
	}
	if err := s.SetGone(ctx, "movie", "3", "2", at); err != nil {
		t.Fatal(err)
	}
	if _, gone, _ := s.GoneOf(ctx, "movie", "2"); gone {
		t.Fatal("2 came back but is still gone")
	}
	if k := s.CurrentKey(ctx, "movie/tmdb/3"); k != "movie/tmdb/2" {
		t.Fatalf("back: %s", k)
	}
	if err := s.MoveGone(ctx, "movie", "9", "", at); err != nil {
		t.Fatal(err)
	}
	if now, gone, _ := s.GoneOf(ctx, "movie", "9"); !gone || now != "" {
		t.Fatalf("unknown: %q %v", now, gone)
	}
	if k := s.CurrentKey(ctx, "movie/tmdb/9"); k != "movie/tmdb/9" {
		t.Fatalf("unknown key: %s", k)
	}
}

// A series' episodes, kept by their IDs, move by their numbers: the new
// series' episodes have other IDs.
func TestMoveGoneEpisodeIDs(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, p, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	at := time.Unix(1_700_000_000, 0)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO own_episodes (series, season, episode, tmdb_id) VALUES
		('10', 1, 1, 501), ('10', 1, 2, 502), ('20', 1, 1, 601), ('20', 1, 2, 602)`); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPlay(ctx, p.ID, "series/tmdb/10/e501", "", 0, true, at); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveGone(ctx, "series", "10", "20", at); err != nil {
		t.Fatal(err)
	}
	if k := s.CurrentKey(ctx, "series/tmdb/10/e502"); k != "series/tmdb/20/e602" {
		t.Fatalf("late key: %s", k)
	}
	got, err := s.PersonalStates(ctx, p.ID, []string{"series/tmdb/20/e601", "series/tmdb/10/e501"})
	if err != nil {
		t.Fatal(err)
	}
	if !got["series/tmdb/20/e601"].Played || !got["series/tmdb/10/e501"].Played {
		t.Fatalf("episode: %+v", got)
	}
}

// A paired server whose title comes back under another key with the same
// IMDb ID (its TMDB merged the entry): what profiles did with it follows.
// A library title under another ID with that IMDb ID is fetched again.
func TestPeerKeyFollows(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, p, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	at := time.Unix(1_700_000_000, 0)
	peer, err := s.AddPeer(ctx, []byte("key"), "peer.example", "Peer", nil)
	if err != nil {
		t.Fatal(err)
	}
	imdb := map[string]string{"imdb": "tt0000001"}
	if err := s.SetPeerTitles(ctx, peer.ID, []PeerTitle{{Key: "movie/tmdb/100", Kind: "movie", Library: "l", IDs: imdb},
		{Key: "movie/tmdb/7", Kind: "movie", Library: "l"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPlay(ctx, p.ID, "movie/tmdb/100", "", 0, true, at); err != nil {
		t.Fatal(err)
	}
	if err := s.AddWish(ctx, p.ID, "movie/tmdb/100"); err != nil {
		t.Fatal(err)
	}
	// The library's copy of another film, under the ID its TMDB dropped.
	if err := s.PutCatalog(ctx, []CatalogItem{{ID: "00000000000000000000000000000001", Kind: "movie", Name: "Other",
		SortName: "other", TMDB: "300", IMDb: "tt0000003"}}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO artwork_fetched (key, fetched_at) VALUES ('movie/tmdb/300', ?)`, at.Unix()); err != nil {
		t.Fatal(err)
	}

	if err := s.SetPeerTitles(ctx, peer.ID, []PeerTitle{{Key: "movie/tmdb/200", Kind: "movie", Library: "l", IDs: imdb},
		{Key: "movie/tmdb/400", Kind: "movie", Library: "l", IDs: map[string]string{"imdb": "tt0000003"}}}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.PersonalStates(ctx, p.ID, []string{"movie/tmdb/200"})
	if !got["movie/tmdb/200"].Played {
		t.Fatalf("followed: %+v", got)
	}
	if w, _ := s.Wishes(ctx, p.ID); len(w) != 1 || w[0].Title != "movie/tmdb/200" {
		t.Fatalf("wish: %v", wishTitles(w))
	}
	// Read before the library had it: still found, at any pass.
	if n, err := s.RefetchIMDbConflicts(ctx, at.Add(time.Second)); err != nil || n != 1 {
		t.Fatalf("refetched: %d %v", n, err)
	}
	var fetched int
	s.read.QueryRowContext(ctx, `SELECT count(*) FROM artwork_fetched WHERE key = 'movie/tmdb/300'`).Scan(&fetched)
	if fetched != 0 {
		t.Fatal("the library's title isn't fetched again")
	}
	// Fetched since: left alone.
	s.db.ExecContext(ctx, `INSERT INTO artwork_fetched (key, fetched_at) VALUES ('movie/tmdb/300', ?)`, at.Add(time.Hour).Unix())
	if n, _ := s.RefetchIMDbConflicts(ctx, at.Add(time.Second)); n != 0 {
		t.Fatalf("fetched lately, refetched again: %d", n)
	}
}

// Proposal 17 sends no IDs with a TMDB key: the paired server's title
// under another key follows by the IMDb IDs Overfin holds for both.
func TestPeerKeyFollowsOwnIMDb(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, p, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	peer, err := s.AddPeer(ctx, []byte("key"), "peer.example", "Peer", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.db.ExecContext(ctx, `INSERT INTO catalog_titles (kind, tmdb, name, overview, status, seen_at, imdb)
		VALUES ('movie', '500', 'Old', '', 0, 0, 'tt0000005'), ('movie', '600', 'New', '', 0, 0, 'tt0000005')`)
	if err := s.SetPeerTitles(ctx, peer.ID, []PeerTitle{{Key: "movie/tmdb/500", Kind: "movie", Library: "l"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPlay(ctx, p.ID, "movie/tmdb/500", "", 0, true, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPeerTitles(ctx, peer.ID, []PeerTitle{{Key: "movie/tmdb/600", Kind: "movie", Library: "l"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.PersonalStates(ctx, p.ID, []string{"movie/tmdb/600"}); !got["movie/tmdb/600"].Played {
		t.Fatalf("not followed: %+v", got)
	}
}

// What a paired server sends is kept under the keys Overfin knows (D10):
// a series keyed by TheTVDB under its TMDB ID, its episodes by their TMDB
// IDs or their IMDb IDs; its extras, local titles, seasons in TheTVDB's
// order, a series TMDB doesn't know and an episode matching none of
// Overfin's, skipped quietly. The engine reads them back by the paired
// server's keys, with their copies; kinds come from keys.
func TestPeerTitlesKeys(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	peer, err := s.AddPeer(ctx, []byte("key"), "peer.example", "Peer", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPeerState(ctx, peer.ID, PeerTheirs, PeerPaired)
	s.db.ExecContext(ctx, `INSERT INTO episode_imdb (series, tmdb_id, imdb, checked_at) VALUES ('1399', 63057, 'tt1668746', 0)`)
	err = s.SetPeerTitles(ctx, peer.ID, []PeerTitle{
		{Key: "movie/tmdb/1/x/" + strings.Repeat("a", 22), Kind: "extra", Library: "l"},
		{Key: "movie/local/" + strings.Repeat("b", 22), Kind: "movie", Library: "l"},
		{Key: "series/tvdb/121361", Kind: "series", Library: "l", IDs: map[string]string{"tmdb": "1399", "imdb": "tt0944947"}},
		{Key: "series/tvdb/121361/s1", Kind: "season", Library: "l"},
		{Key: "series/tvdb/121361/s1/e1", Kind: "episode", Library: "l", Audio: []string{"en"}, IDs: map[string]string{"tmdb": "63056"}},
		{Key: "series/tvdb/121361/s1/e2", Kind: "episode", Library: "l", Audio: []string{"en"}, IDs: map[string]string{"imdb": "tt1668746"}},
		{Key: "series/tvdb/121361/s1/e3", Kind: "episode", Library: "l", Audio: []string{"en"}, IDs: map[string]string{"imdb": "tt9999999"}},
		{Key: "series/tvdb/7", Kind: "series", Library: "l"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	s.each(ctx, func(rows *sql.Rows) error {
		var k string
		rows.Scan(&k)
		kept = append(kept, k)
		return nil
	}, `SELECT title_key || '<' || coalesce(peer_key, '') || '<' || kind FROM peer_titles WHERE peer_id = ? ORDER BY title_key`, peer.ID)
	want := []string{"series/tmdb/1399<series/tvdb/121361<series", "series/tmdb/1399/e63056<series/tvdb/121361/s1/e1<episode",
		"series/tmdb/1399/e63057<series/tvdb/121361/s1/e2<episode"}
	if !slices.Equal(kept, want) {
		t.Fatalf("kept %v, want %v", kept, want)
	}
	var back msfp.Title
	s.MSFP().Read(ctx, func(tx msfp.Tx) error {
		back, err = tx.PeerTitle(peer.ID, "series/tvdb/121361/s1/e2")
		return err
	})
	if v, ok := back.(*msfp.VideoTitle); !ok || v.Key != "series/tvdb/121361/s1/e2" || len(v.Copies) != 1 {
		t.Fatalf("read back: %+v", back)
	}
	sources, err := s.PeerSources(ctx, "series/tmdb/1399/e63056")
	if err != nil || len(sources) != 1 || sources[0].Key != "series/tvdb/121361/s1/e1" || len(sources[0].Copies) != 1 {
		t.Fatalf("sources %+v %v", sources, err)
	}
	// Dropped by the paired server's key.
	if err := s.MSFP().Update(ctx, func(tx msfp.Tx) error {
		return tx.DropPeerTitles(peer.ID, []string{"series/tvdb/121361/s1/e1"})
	}); err != nil {
		t.Fatal(err)
	}
	if sources, _ := s.PeerSources(ctx, "series/tmdb/1399/e63056"); len(sources) != 0 {
		t.Fatalf("still there: %+v", sources)
	}
}
