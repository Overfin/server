package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/msfp-io/msfp-go"
	"github.com/msfp-io/msfp-go/wire"
	"github.com/overfin/server/internal/api"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/store"
)

// Overfin as the host of the MSFP engine (package msfp, whose own tests
// cover the protocol): its titles listed to paired servers and played
// for them through its playback (api.PeerHost), and theirs listed and
// played on its site, through it.

// Members by their exact names: a play's answer, its stream built from
// its ticket under this server's own route.
type peerSession struct {
	ID, Ticket, Protocol, Method, VersionID, AudioTrack, Etag string
	Subtitles                                                 []wire.ServedSubtitle
	Type, Detail                                              string // a problem's
}

// URL is the playback's stream, as a player builds it (MSFP: from the
// ticket).
func (s peerSession) URL() string {
	name := "master.m3u8"
	if s.Protocol == "file" {
		name = "file"
	}
	return "/api/v1/stream/" + s.Ticket + "/" + name
}

// twoServers is B, holding a film, Heat (the copy index's copy, played by
// its executor: playedServer), paired with A, which wants B's films and
// knows of Heat but holds no copy of it.
type twoServers struct {
	srvA, srvB *httptest.Server
	played     *played
	stA, stB   *store.Store
	a, b       *msfp.Engine
	onB        *playback.Tickets
	aOnB, bOnA msfp.PeerView
	pbA, pbB   api.Playback
	// endedOnB are B's engine's endings of A's playbacks, as they come
	// (the first 16).
	endedOnB chan msfp.PlaybackEnded
}

// pairForPlay makes twoServers.
func pairForPlay(t *testing.T) *twoServers {
	t.Helper()
	return pairForPlayWith(t, nil)
}

// pairForPlayWith is pairForPlay with B's handler wrapped by wrapB.
func pairForPlayWith(t *testing.T, wrapB func(http.Handler) http.Handler) *twoServers {
	t.Helper()
	ctx := context.Background()
	x := twoServers{endedOnB: make(chan msfp.PlaybackEnded, 16)}
	x.played = playedServer(t, options{federation: &x.b, playback: &x.pbB, wrap: wrapB,
		events: func(ev msfp.Event) {
			if e, ok := ev.(msfp.PlaybackEnded); ok {
				select {
				case x.endedOnB <- e:
				default: // a test not listening
				}
			}
		}})
	x.srvB, x.stB, x.onB = x.played.srv, x.played.st, x.played.tickets
	id := func(n int) string { return fmt.Sprintf("%032x", n) }
	x.stB.SetLibraries(ctx, []store.Library{{ID: id(0xf1), Item: []byte(`{"Name":"Films","CollectionType":"movies"}`)},
		{ID: id(0xf2), Item: []byte(`{"Name":"Songs","CollectionType":"music"}`)}})
	x.srvA, _ = newServerWith(t, options{store: &x.stA, federation: &x.a, playback: &x.pbA, fedName: "A"})
	x.aOnB, x.bOnA = pairEngines(t, x.b, x.a)
	if err := x.b.SetOffers(ctx, x.aOnB.ID, []string{id(0xf1)}); err != nil {
		t.Fatal(err)
	}
	wantAll(t, x.a, x.bOnA.ID)
	x.stA.SetCatalogState(ctx, store.CatalogState{Pass: 1})
	x.stA.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "949", Name: "Heat", Year: 1995}, []byte(`{}`), time.Now())
	return &x
}

// putCopy puts a read copy of an item in the copy index, of parts files.
func putCopy(t *testing.T, st *store.Store, item, copyID string, parts int) {
	t.Helper()
	c := store.IndexedCopy{CopyID: copyID, ItemID: item, SourceID: item, Fingerprint: "x1-" + item, ETag: "etag" + item[14:],
		Size: 1000, Parts: parts, Streams: map[string]int{"v": 0, "a1": 1},
		Facts: wire.Copy{DurationMs: 60_000, MediaType: "video/matroska", Size: 1000,
			AudioTracks: []wire.CopyAudio{{ID: "a1", Codec: "aac", Codecs: "mp4a.40.2", Channels: 2, Language: "en"}}}}
	if err := st.PutCopy(context.Background(), c); err != nil {
		t.Fatal(err)
	}
}

// pairEngines pairs b with owner: owner makes a code, b redeems it,
// owner accepts. It gives each one's view of the other.
func pairEngines(t *testing.T, owner, b *msfp.Engine) (bOnOwner, ownerOnB msfp.PeerView) {
	t.Helper()
	ctx := context.Background()
	code, _, err := owner.MakeCode(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	ownerOnB, err = b.RedeemCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	peers, _ := owner.Peers(ctx)
	if bOnOwner, err = owner.Accept(ctx, peers[0].ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if ownerOnB, err = b.Peer(ctx, ownerOnB.ID); err == nil && ownerOnB.State == msfp.Paired {
			return bOnOwner, ownerOnB
		}
		if time.Now().After(deadline) {
			t.Fatalf("not paired: %+v %v", ownerOnB, err)
		}
		b.Check(ctx, ownerOnB.ID)
		time.Sleep(20 * time.Millisecond)
	}
}

// wantAll has e want every library peer offers it, and read them.
func wantAll(t *testing.T, e *msfp.Engine, peer string) {
	t.Helper()
	ctx := context.Background()
	libs, err := e.TheirLibraries(ctx, peer)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, l := range libs {
		ids = append(ids, l.ID)
	}
	if err := e.SetWants(ctx, peer, ids); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ReadNow(ctx, peer); err != nil {
		t.Fatal(err)
	}
}

// viewerGet is the viewer's browser on A asking A.
func viewerGet(t *testing.T, srvA *httptest.Server, path string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", srvA.URL+path, nil)
	req.Header.Set("Remote-User", subject)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	res, err := client(srvA, subject).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	return res.StatusCode, string(b)
}

// livePlaybacks are a server's playbacks.
func livePlaybacks(t *playback.Tickets) []*playback.Stream {
	return t.Where(func(*playback.Stream) bool { return true })
}

// startOnA is A's viewer's play of Heat, with MSFP's play request body.
func (x *twoServers) start(t *testing.T, body string) (int, peerSession) {
	t.Helper()
	status, raw := post(t, x.srvA, "/api/v1/playback", body)
	var s peerSession
	json.Unmarshal([]byte(raw), &s)
	if status == 201 && (strings.Contains(raw, x.srvB.URL) || !strings.HasPrefix(s.ID, "play_")) {
		t.Fatalf("play on A: %s", raw)
	}
	return status, s
}

// Someone on A plays a film only B has: A's title page lists B's copy,
// under an ID of A's, said to be a paired server's; the play asks B with
// the player's request, the stream comes through A (the browser never
// asks B, nor gets B's ticket), B plays it through its executor (here
// the baseline: the H.264 picture copied, its E-AC-3 converted), A keeps
// where they are, B keeps nothing personal, another audio track under a
// 1-stream limit replaces the playback there, and stopping on A ends it
// on B.
func TestPlayFromPeer(t *testing.T) {
	ctx := context.Background()
	x := pairForPlay(t)
	srvA, stB, onB := x.srvA, x.stB, x.onB

	var page struct {
		Playable bool
		Copies   []wire.Copy
		Peers    []string
	}
	if status := get(t, srvA, "/api/v1/titles/title_movie_tmdb_949", &page); status != 200 || !page.Playable ||
		len(page.Copies) != 1 || !slices.Equal(page.Peers, []string{page.Copies[0].ID}) {
		t.Fatalf("A's title page: %d %+v", status, page)
	}
	c := page.Copies[0]
	if c.ID == heatCopy || !strings.HasPrefix(c.ID, "v") || len(c.AudioTracks) != 2 || c.Video == nil || c.Video.Height != 1080 ||
		c.PreviewLayout == nil || len(c.Segments) != 2 {
		t.Fatalf("B's copy on A: %+v", c)
	}

	status, s := x.start(t, `{"titleId":"title_movie_tmdb_949","versionId":"`+c.ID+`"}`)
	if status != 201 || s.VersionID != c.ID || s.Method != "transcode" || s.Protocol != "hls" || s.AudioTrack != "a1" || s.Etag == "" {
		t.Fatalf("play on A: %d %+v", status, s)
	}
	if o := x.played.maker.last(); !o.Video.Copy || o.Audio.Copy || o.Audio.Codec != "aac" {
		t.Fatalf("B's order: %+v", o)
	}
	// Subtitles served apart: SubRip as text, PGS as pictures; DVD
	// pictures never apart (B's Describe).
	if !slices.Equal(s.Subtitles, []wire.ServedSubtitle{{ID: "s3", Form: "text"}, {ID: "s4", Form: "picture"}}) {
		t.Errorf("subtitles: %+v", s.Subtitles)
	}
	if code, got := viewerGet(t, srvA, s.URL()); code != 200 || !strings.HasPrefix(got, "#EXTM3U") {
		t.Fatalf("the stream through A: %d %q", code, got)
	}
	// On B, a playback of A's, counted as the pairing's: B's ticket stays
	// between the servers.
	live := livePlaybacks(onB)
	if len(live) != 1 || !strings.HasPrefix(live[0].AccountID, playback.PeerPrefix) || live[0].Delivery == nil {
		t.Fatalf("playbacks on B: %d", len(live))
	}
	if live[0].Ticket == s.Ticket {
		t.Fatal("the viewer got B's ticket")
	}
	if views := x.b.Playbacks(); len(views) != 1 || views[0].Handle != live[0].ID || views[0].PeerName != "A" {
		t.Fatalf("B's engine's playbacks: %+v", views)
	}
	if status, body := post(t, srvA, "/api/v1/playback/"+s.ID+"/progress", `{"positionMs":600000}`); status != 204 {
		t.Fatalf("progress: %d %s", status, body)
	}
	// Another audio track, with one stream for the pairing: the browser's
	// start comes before its stop of the old playback, which ends first.
	if err := x.b.SetLimits(ctx, x.aOnB.ID, msfp.Limits{Streams: 1, Transcodes: 1}); err != nil {
		t.Fatal(err)
	}
	status, restarted := x.start(t, `{"titleId":"title_movie_tmdb_949","versionId":"`+c.ID+`","audioTrack":"a2"}`)
	if status != 201 || restarted.AudioTrack != "a2" || restarted.Method != "remux" {
		t.Fatalf("a restart under a 1-stream limit: %d %+v", status, restarted)
	}
	if code, _ := viewerGet(t, srvA, s.URL()); code != http.StatusNotFound {
		t.Errorf("the old playback's stream after the restart: %d", code)
	}
	if n := len(livePlaybacks(onB)); n != 1 {
		t.Errorf("playbacks on B after the restart: %d", n)
	}
	if code, _ := viewerGet(t, srvA, restarted.URL()); code != 200 {
		t.Errorf("the new playback's stream: %d", code)
	}
	if status, _ := post(t, srvA, "/api/v1/playback/"+restarted.ID+"/stop", `{"positionMs":600000}`); status != 204 {
		t.Fatalf("stop: %d", status)
	}
	waitFor(t, "B's playbacks after A stopped", func() bool { return len(livePlaybacks(onB)) == 0 && len(x.b.Playbacks()) == 0 })
	// Nothing personal kept on B, once stopped mid-film (recording runs
	// in the background).
	time.Sleep(300 * time.Millisecond)
	if got, _ := stB.ContinueWatching(ctx, playback.PeerPrefix+x.aOnB.ID, 0, 10); len(got) != 0 {
		t.Fatalf("B kept a position: %v", got)
	}
	// A copy B's catalog doesn't have (any more): not found, for the
	// player to read the title again.
	if status, _ := x.start(t, `{"titleId":"title_movie_tmdb_949","versionId":"vnonenonenonenonenonenonenone"}`); status != http.StatusNotFound {
		t.Errorf("a copy of none: %d", status)
	}
}

// The copy's file, through A by byte ranges (the engine's remote file),
// with the copy's media type and etag; a read cut there (B's playback
// ended: a long pause) reads on under a new playback of the same copy.
func TestPeerFileRelayed(t *testing.T) {
	x := pairForPlay(t)
	var page struct{ Copies []wire.Copy }
	get(t, x.srvA, "/api/v1/titles/title_movie_tmdb_949", &page)
	status, s := x.start(t, `{"titleId":"title_movie_tmdb_949","versionId":"`+page.Copies[0].ID+`","delivery":"file"}`)
	if status != 201 || s.Protocol != "file" || s.Method != "direct" {
		t.Fatalf("file: %d %+v", status, s)
	}
	ranged := func(want int) {
		t.Helper()
		req, _ := http.NewRequest("GET", x.srvA.URL+s.URL(), nil)
		req.Header.Set("Remote-User", subject)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Range", "bytes=10-19")
		res, err := client(x.srvA, subject).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want || want == 206 && (string(b) != string(x.played.file[10:20]) ||
			res.Header.Get("Content-Range") != fmt.Sprintf("bytes 10-19/%d", len(x.played.file)) ||
			res.Header.Get("Content-Type") != "video/matroska") {
			t.Fatalf("a range through A: %d %q %v", res.StatusCode, b, res.Header)
		}
	}
	ranged(206)
	// B ends it (its idle cleanup): A reads on under a new playback.
	x.pbB.EndIdle(time.Now().Add(api.IdleTimeout + time.Minute))
	if r := x.ending(t); r != msfp.EndExpired {
		t.Fatalf("B's idle cleanup: %v", r)
	}
	ranged(206)
	if n := len(livePlaybacks(x.onB)); n != 1 {
		t.Fatalf("B's playbacks after the restart: %d", n)
	}
}

// Un-offering the library, or ending the pairing, ends a paired server's
// playbacks here at once, both sides: A's viewer's stream stops (MSFP
// playback, section 6.3).
func TestPeerPlaybacksEndAtOnce(t *testing.T) {
	ctx := context.Background()
	x := pairForPlay(t)
	films := fmt.Sprintf("%032x", 0xf1)
	s := x.playPeer(t)
	if err := x.b.SetOffers(ctx, x.aOnB.ID, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the library no longer offered: B's playback still live", func() bool { return len(livePlaybacks(x.onB)) == 0 })
	if code, _ := viewerGet(t, x.srvA, s.URL()); code != http.StatusNotFound {
		t.Fatalf("the library no longer offered: the stream through A %d", code)
	}
	if err := x.b.SetOffers(ctx, x.aOnB.ID, []string{films}); err != nil {
		t.Fatal(err)
	}
	wantAll(t, x.a, x.bOnA.ID)
	s = x.playPeer(t)
	if err := x.a.End(ctx, x.bOnA.ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := viewerGet(t, x.srvA, s.URL()); code != http.StatusNotFound {
		t.Fatalf("the pairing ended: the stream through A %d", code)
	}
	var mine []*playback.Stream
	for _, st := range livePlaybacks(x.pbA.Tickets) {
		if st.Remote != nil {
			mine = append(mine, st)
		}
	}
	if len(mine) != 0 {
		t.Fatalf("A's playbacks from B after the pairing ended: %d", len(mine))
	}
}

// A's site lists what B holds for it, beside its own: the library taken
// from B is an entry of the menu, "Films (B)" (or A's own name for it),
// listing B's films; all titles list both films, each once.
func TestPeerTitlesListed(t *testing.T) {
	ctx := context.Background()
	var stB, stA *store.Store
	var b, a *msfp.Engine
	newServerWith(t, options{store: &stB, federation: &b, fedName: "Bee"})
	stB.PutCatalog(ctx, []store.CatalogItem{{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "Heat", SortName: "heat",
		TMDB: "949", LibraryID: "films"}, {ID: fmt.Sprintf("%032x", 2), Kind: "movie", Name: "Up", SortName: "up",
		TMDB: "14160", LibraryID: "films"}}, 1)
	stB.RefreshTitleKeys(ctx)
	putCopy(t, stB, fmt.Sprintf("%032x", 1), "vheatheatheatheatheatheathe", 1)
	putCopy(t, stB, fmt.Sprintf("%032x", 2), "vupupupupupupupupupupupupup", 1)
	stB.SetLibraries(ctx, []store.Library{{ID: "films", Item: []byte(`{"Name":"Films","CollectionType":"movies"}`)},
		{ID: "songs", Item: []byte(`{"Name":"Songs","CollectionType":"music"}`)}})
	srvA, _ := newServerWith(t, options{store: &stA, federation: &a, fedName: "A"})
	// A has Up itself; it keeps TMDB's pages of both.
	stA.PutCatalog(ctx, []store.CatalogItem{{ID: fmt.Sprintf("%032x", 9), Kind: "movie", Name: "Up", SortName: "up",
		TMDB: "14160", LibraryID: "mine"}}, 1)
	stA.RefreshTitleKeys(ctx)
	stA.SetCatalogState(ctx, store.CatalogState{Pass: 1})
	now := time.Now()
	stA.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "949", Name: "Heat", Year: 1995}, []byte(`{}`), now)
	stA.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "14160", Name: "Up", Year: 2009}, []byte(`{}`), now)

	aOnB, bOnA := pairEngines(t, b, a)
	// B's libraries of video only: Overfin shares no music.
	offers, err := b.Offers(ctx, aOnB.ID)
	if err != nil || len(offers) != 1 || offers[0].ID != "films" {
		t.Fatalf("B's libraries to offer: %+v %v", offers, err)
	}
	if err := b.SetOffers(ctx, aOnB.ID, []string{"films"}); err != nil {
		t.Fatal(err)
	}
	wantAll(t, a, bOnA.ID)
	if src, err := stA.PeerSources(ctx, "movie/tmdb/949"); err != nil || len(src) != 1 || src[0].Peer != bOnA.ID || src[0].Name != "Bee" {
		t.Fatalf("Heat's sources on A: %+v %v", src, err)
	}

	type page struct {
		Items []struct {
			ID, Name, Kind string
		}
		TotalCount int
	}
	var libs page
	get(t, srvA, "/api/v1/libraries", &libs)
	var entry string
	for _, l := range libs.Items {
		if l.Name == "Films (Bee)" && l.Kind == "movies" {
			entry = l.ID
		}
	}
	if entry == "" {
		t.Fatalf("A's menu: %+v", libs.Items)
	}
	names := func(path string) []string {
		t.Helper()
		var p page
		if status := get(t, srvA, path, &p); status != 200 {
			t.Fatalf("%s: %d", path, status)
		}
		var out []string
		for _, it := range p.Items {
			out = append(out, it.Name)
		}
		if p.TotalCount != len(out) {
			t.Fatalf("%s: total %d for %v", path, p.TotalCount, out)
		}
		return out
	}
	if got := names("/api/v1/titles?library=" + entry + "&kind=movie&kind=series&sort=name"); len(got) != 2 || got[0] != "Heat" || got[1] != "Up" {
		t.Fatalf("B's library on A: %v", got)
	}
	if got := names("/api/v1/titles?kind=movie&kind=series&sort=name"); len(got) != 2 || got[0] != "Heat" || got[1] != "Up" {
		t.Fatalf("all titles on A (each once): %v", got)
	}
	// A's own name for the entry.
	wants, _ := a.Wants(ctx, bOnA.ID)
	if err := stA.LabelPeerWant(ctx, bOnA.ID, wants[0].ID, "Their films"); err != nil {
		t.Fatal(err)
	}
	get(t, srvA, "/api/v1/libraries", &libs)
	if !slices.ContainsFunc(libs.Items, func(l struct{ ID, Name, Kind string }) bool { return l.Name == "Their films" && l.ID == entry }) {
		t.Fatalf("renamed: %+v", libs.Items)
	}
	// Not wanted any more: its entry and its titles go at once.
	if err := a.SetWants(ctx, bOnA.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got := names("/api/v1/titles?kind=movie&kind=series&sort=name"); len(got) != 1 || got[0] != "Up" {
		t.Fatalf("all titles on A, B's library no longer wanted: %v", got)
	}
}

// B refusing a playback tells A why, and A its viewer: a delivery B
// won't make is refused (the player steps down), B's limit for A reached
// is busy.
func TestPeerRefusalsToldApart(t *testing.T) {
	ctx := context.Background()
	x := pairForPlay(t)
	var page struct{ Copies []wire.Copy }
	get(t, x.srvA, "/api/v1/titles/title_movie_tmdb_949", &page)
	v := page.Copies[0].ID
	if status, s := x.start(t, `{"titleId":"title_movie_tmdb_949","versionId":"`+v+`","delivery":"transcode","transcode":{"videoCodec":"av1"}}`); status != http.StatusConflict || s.Type != "delivery-refused" {
		t.Fatalf("a codec B doesn't make: %d %+v", status, s)
	}
	if n := len(livePlaybacks(x.onB)); n != 0 {
		t.Errorf("playbacks on B: %d", n)
	}
	if err := x.b.SetLimits(ctx, x.aOnB.ID, msfp.Limits{}); err != nil {
		t.Fatal(err)
	}
	if status, s := x.start(t, `{"titleId":"title_movie_tmdb_949","versionId":"`+v+`"}`); status != http.StatusServiceUnavailable || s.Type != "server-busy" {
		t.Fatalf("B's limit for A reached: %d %+v", status, s)
	}
}

// A copy of several files (Jellyfin's stacked parts) is one copy in MSFP,
// to be served whole; B can't serve it as one, so it isn't offered: a
// film B holds only so has no copy on A.
func TestPeerCopyOfSeveralParts(t *testing.T) {
	ctx := context.Background()
	x := pairForPlay(t)
	putCopy(t, x.stB, fmt.Sprintf("%032x", 3), upCopy, 2) // Up, now two files
	x.stA.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "14160", Name: "Up", Year: 2009}, []byte(`{}`), time.Now())
	if _, err := x.a.ReadNow(ctx, x.bOnA.ID); err != nil {
		t.Fatal(err)
	}
	var page struct{ Copies []wire.Copy }
	if get(t, x.srvA, "/api/v1/titles/title_movie_tmdb_14160", &page); len(page.Copies) != 0 {
		t.Fatalf("a copy of two parts offered: %+v", page.Copies)
	}
	if status, _ := x.start(t, `{"titleId":"title_movie_tmdb_14160"}`); status != http.StatusNotFound {
		t.Fatalf("a copy of two parts: %d", status)
	}
}

// B's host refusing A's play unsigned (a gateway's plain 429) is busy
// for now to A's viewer, not "can't be played".
func TestPeerPlayUnansweredIsBusy(t *testing.T) {
	x := pairForPlayWith(t, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/federation/play" {
				w.Header().Set("Retry-After", "5")
				http.Error(w, "Too many requests. Try again in a moment.", http.StatusTooManyRequests)
				return
			}
			h.ServeHTTP(w, r)
		})
	})
	if status, s := x.start(t, `{"titleId":"title_movie_tmdb_949"}`); status != http.StatusServiceUnavailable || s.Type != "server-busy" {
		t.Fatalf("an unsigned 429 from B's host: %d %+v", status, s)
	}
}

// B says it can't be called (MSFP core section 7.5): A connects to it no
// more, its viewers' playback from B among them, which ends at once.
func TestRemotePlaybackEndsWhenPeerUncallable(t *testing.T) {
	ctx := context.Background()
	x := pairForPlay(t)
	s := x.playPeer(t)
	if err := x.b.SetUncallable(ctx, true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B still callable on A", func() bool {
		p, _ := x.a.Peer(ctx, x.bOnA.ID)
		return p.Inbound == msfp.InboundNo
	})
	if code, _ := viewerGet(t, x.srvA, s.URL()); code != http.StatusNotFound {
		t.Fatalf("A still relays B's stream, B saying it can't be called: %d", code)
	}
}

// playPeer starts A's viewer's playback of B's film (its first copy, the
// baseline), and fetches its stream once.
func (x *twoServers) playPeer(t *testing.T) peerSession {
	t.Helper()
	status, s := x.start(t, `{"titleId":"title_movie_tmdb_949"}`)
	if status != 201 {
		t.Fatalf("play: %d %+v", status, s)
	}
	if code, _ := viewerGet(t, x.srvA, s.URL()); code != 200 {
		t.Fatalf("the stream: %d", code)
	}
	return s
}

// ending waits for B's engine's next ending of a playback of A's.
func (x *twoServers) ending(t *testing.T) msfp.EndReason {
	t.Helper()
	select {
	case ev := <-x.endedOnB:
		return ev.Reason
	case <-time.After(5 * time.Second):
		t.Fatal("B's playback didn't end")
		return ""
	}
}

// waitFor waits until ok says yes.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A's viewer's progress reaches B at the first report: B's live view
// (the control panel's Now playing) shows where the paired server's
// viewer is (MSFP's progress carries no pause: shown playing).
func TestPeerProgressShownByOwner(t *testing.T) {
	x := pairForPlay(t)
	s := x.playPeer(t)
	onB := func(positionMs int64) func() bool {
		return func() bool {
			live := x.onB.Live(time.Now())
			return len(live) == 1 && live[0].PositionMs == positionMs && !live[0].Paused
		}
	}
	if status, body := post(t, x.srvA, "/api/v1/playback/"+s.ID+"/progress", `{"positionMs":600000}`); status != 204 {
		t.Fatalf("progress: %d %s", status, body)
	}
	waitFor(t, "B's live view after the first report", onB(600_000))
	if status, _ := post(t, x.srvA, "/api/v1/playback/"+s.ID+"/progress", `{"positionMs":610000,"paused":true}`); status != 204 {
		t.Fatal("progress")
	}
	waitFor(t, "B's live view after a pause", onB(610_000))
	if v := x.b.Playbacks(); len(v) != 1 || v[0].State != "active" {
		t.Errorf("B's engine's playbacks: %+v", v)
	}
}

// Every way A ends its viewer's playback of B's film tells B at once
// (stop): the viewer's player stopping, A's idle cleanup, the viewer
// signing out.
func TestPeerPlaybackEndsWithViewer(t *testing.T) {
	x := pairForPlay(t)
	s := x.playPeer(t)
	if status, _ := post(t, x.srvA, "/api/v1/playback/"+s.ID+"/stop", `{"positionMs":5000}`); status != 204 {
		t.Fatal("stop")
	}
	if r := x.ending(t); r != msfp.EndStopped {
		t.Errorf("the player stopped: %v", r)
	}
	x.playPeer(t)
	x.pbA.EndIdle(time.Now().Add(api.IdleTimeout + time.Minute))
	if r := x.ending(t); r != msfp.EndStopped {
		t.Errorf("A's idle cleanup: %v", r)
	}
	x.playPeer(t)
	mine := livePlaybacks(x.pbA.Tickets)
	if len(mine) != 1 {
		t.Fatalf("A's playbacks: %d", len(mine))
	}
	x.pbA.EndOf(mine[0].AccountID, mine[0].SessionID) // signing out
	if r := x.ending(t); r != msfp.EndStopped {
		t.Errorf("signed out: %v", r)
	}
	waitFor(t, "B's playbacks after A's endings", func() bool { return len(livePlaybacks(x.onB)) == 0 && len(x.b.Playbacks()) == 0 })
}

// B ending A's playback by its own rules says why: its idle cleanup is
// expiry, the operator's stop the operator's; A's viewer's player stops
// at its next report.
func TestPeerPlaybackEndedByOwner(t *testing.T) {
	x := pairForPlay(t)
	s := x.playPeer(t)
	x.pbB.EndIdle(time.Now().Add(api.IdleTimeout + time.Minute))
	if r := x.ending(t); r != msfp.EndExpired {
		t.Errorf("B's idle cleanup: %v", r)
	}
	post(t, x.srvA, "/api/v1/playback/"+s.ID+"/progress", `{"positionMs":1000}`)
	waitFor(t, "A's playback after B's ending", func() bool {
		status, _ := viewerGet(t, x.srvA, "/api/v1/playback/"+s.ID)
		return status == http.StatusGone
	})
	x.playPeer(t)
	live := livePlaybacks(x.onB)
	if len(live) != 1 || !x.pbB.EndByAdmin(live[0].ID) {
		t.Fatalf("B's playbacks: %d", len(live))
	}
	if r := x.ending(t); r != msfp.EndOperator {
		t.Errorf("B's operator: %v", r)
	}
}

// Downloads and scans of B's copies, within the pairing's limits only (0,
// none, by default: D13), kept for their late reports (msfp_downloads);
// a burned subtitle never without B's operator's switch.
func TestPeerDownloadsWithinLimits(t *testing.T) {
	ctx := context.Background()
	x := pairForPlay(t)
	key := "movie/tmdb/949"
	if _, err := x.a.Play(ctx, x.bOnA.ID, msfp.PlayRequest{Title: key, Purpose: msfp.PurposeDownload, Delivery: msfp.DeliveryFile}); err == nil {
		t.Fatal("a download with no downloads allowed")
	}
	if _, err := x.a.Play(ctx, x.bOnA.ID, msfp.PlayRequest{Title: key, Purpose: msfp.PurposeScan, Delivery: msfp.DeliveryFile}); err == nil {
		t.Fatal("a scan with no scans allowed")
	}
	if err := x.b.SetLimits(ctx, x.aOnB.ID, msfp.Limits{Streams: 2, Transcodes: 1, Downloads: 1, ScansPerDay: 5}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "A told of B's limits", func() bool {
		v, err := x.a.Check(ctx, x.bOnA.ID)
		return err == nil && v.TheirLimits != nil && v.TheirLimits.Downloads == 1
	})
	rp, err := x.a.Play(ctx, x.bOnA.ID, msfp.PlayRequest{Title: key, Purpose: msfp.PurposeDownload, Delivery: msfp.DeliveryFile})
	if err != nil || rp.Protocol != "file" || rp.Size != int64(len(x.played.file)) {
		t.Fatalf("a download: %+v %v", rp, err)
	}
	var kept []msfp.DownloadRecord
	x.stB.MSFP().Read(ctx, func(tx msfp.Tx) error {
		kept, err = tx.(msfp.DownloadsTx).Downloads()
		return err
	})
	if len(kept) != 1 || kept[0].Viewer || kept[0].Peer != x.aOnB.ID {
		t.Fatalf("B's downloads kept: %+v %v", kept, err)
	}
	if _, err := x.a.Play(ctx, x.bOnA.ID, msfp.PlayRequest{Title: key, Purpose: msfp.PurposeDownload, Delivery: msfp.DeliveryFile}); err == nil {
		t.Fatal("a second download at once, one allowed")
	}
	if rp, err := x.a.Play(ctx, x.bOnA.ID, msfp.PlayRequest{Title: key, Purpose: msfp.PurposeScan, Delivery: msfp.DeliveryFile}); err != nil || rp.ScanBytes == 0 {
		t.Fatalf("a scan: %+v %v", rp, err)
	}
	_, err = x.a.Play(ctx, x.bOnA.ID, msfp.PlayRequest{Title: key, Delivery: msfp.DeliveryTranscode,
		Transcode: &msfp.TranscodeTargets{BurnSubtitle: "s3"}})
	if !errors.Is(err, msfp.ErrDeliveryRefused) {
		t.Fatalf("a burned subtitle: %v", err)
	}
}
