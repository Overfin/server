package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/api"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/msfp"
)

// Overfin as the host of the MSFP engine (package msfp, whose own tests
// cover the protocol): its titles listed to paired servers and played
// for them through its playback (api.PeerHost), and theirs listed and
// played on its site, through it.

// Members by their exact names.
type peerSession struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Stream struct {
		URL string `json:"url"`
	} `json:"stream"`
	VersionID string `json:"versionId"`
}

// twoServers is B, holding a film, Heat, paired with A, which wants B's
// films and knows of Heat.
type twoServers struct {
	srvA, srvB *httptest.Server
	fakeB      *fakeJellyfin
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
	x.srvB, x.fakeB = newServerWith(t, options{store: &x.stB, federation: &x.b, tickets: &x.onB, playback: &x.pbB, wrap: wrapB,
		events: func(ev msfp.Event) {
			if e, ok := ev.(msfp.PlaybackEnded); ok {
				select {
				case x.endedOnB <- e:
				default: // a test not listening
				}
			}
		}})
	x.fakeB.setDirect(true)
	x.stB.PutCatalog(ctx, []store.CatalogItem{{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "Heat", SortName: "heat",
		TMDB: "949", LibraryID: "films", AudioTags: []string{"fr"}}}, 1)
	x.stB.RefreshTitleKeys(ctx)
	x.stB.SetLibraries(ctx, []store.Library{{ID: "films", Item: []byte(`{"Name":"Films","CollectionType":"movies"}`)}})
	x.srvA, _ = newServerWith(t, options{store: &x.stA, federation: &x.a, playback: &x.pbA, fedName: "A"})
	x.aOnB, x.bOnA = pairEngines(t, x.b, x.a)
	if err := x.b.SetOffers(ctx, x.aOnB.ID, []string{"films"}); err != nil {
		t.Fatal(err)
	}
	wantAll(t, x.a, x.bOnA.ID)
	x.stA.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "949", Name: "Heat", Year: 1995}, []byte(`{}`), time.Now())
	return &x
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

// Someone on A plays a film only B has: A asks B, the stream comes
// through A (the browser never asks B), A keeps where they are, B keeps
// nothing personal, a restart under a 1-stream limit replaces the
// playback there, and stopping on A ends it on B.
func TestPlayFromPeer(t *testing.T) {
	ctx := context.Background()
	x := pairForPlay(t)
	srvA, srvB, stB, onB := x.srvA, x.srvB, x.stB, x.onB

	// On A (which keeps TMDB's page of it), the film is there to play,
	// though no library of A's has it.
	var title struct {
		Availability string
		Playable     bool
	}
	if status := get(t, srvA, "/api/v1/titles/title_movie_tmdb_949", &title); status != 200 || title.Availability != "available" || !title.Playable {
		t.Fatalf("A's title page: %d %+v", status, title)
	}

	status, body := post(t, srvA, "/api/v1/playback", `{"titleId":"title_movie_tmdb_949","capabilities":{},"audioLanguage":"fr"}`)
	var s peerSession
	json.Unmarshal([]byte(body), &s)
	if status != 201 || !strings.HasPrefix(s.Stream.URL, "/api/v1/stream/") || strings.Contains(body, srvB.URL) || !strings.HasPrefix(s.ID, "play_") {
		t.Fatalf("play on A: %d %s", status, body)
	}
	// Its version, as B names it: an opaque ID of B's, never Jellyfin's.
	if !strings.HasPrefix(s.VersionID, "ver_") || strings.Contains(s.VersionID, fmt.Sprintf("%032x", 1)) {
		t.Fatalf("version: %q", s.VersionID)
	}
	if code, got := viewerGet(t, srvA, s.Stream.URL); code != 200 || got == "" {
		t.Fatalf("the stream through A: %d %q", code, got)
	}
	// On B, a playback of A's, counted as the pairing's: B's ticket stays
	// between the servers.
	live := livePlaybacks(onB)
	if len(live) != 1 || !strings.HasPrefix(live[0].AccountID, playback.PeerPrefix) {
		t.Fatalf("playbacks on B: %d", len(live))
	}
	if strings.Contains(body, live[0].Ticket) {
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
	status, body = post(t, srvA, "/api/v1/playback", `{"titleId":"title_movie_tmdb_949","capabilities":{},"audioLanguage":"fr"}`)
	var restarted peerSession
	json.Unmarshal([]byte(body), &restarted)
	if status != 201 {
		t.Fatalf("a restart under a 1-stream limit: %d %s", status, body)
	}
	if code, _ := viewerGet(t, srvA, s.Stream.URL); code != http.StatusNotFound {
		t.Errorf("the old playback's stream after the restart: %d", code)
	}
	if n := len(livePlaybacks(onB)); n != 1 {
		t.Errorf("playbacks on B after the restart: %d", n)
	}
	if code, _ := viewerGet(t, srvA, restarted.Stream.URL); code != 200 {
		t.Errorf("the new playback's stream: %d", code)
	}
	if status, _ := post(t, srvA, "/api/v1/playback/"+restarted.ID+"/stop", `{"positionMs":600000}`); status != 204 {
		t.Fatalf("stop: %d", status)
	}
	// B hears of the stop: its playback ends.
	deadline := time.Now().Add(5 * time.Second)
	for len(livePlaybacks(onB)) != 0 || len(x.b.Playbacks()) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("B's playbacks after A stopped: %d", len(livePlaybacks(onB)))
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Nothing personal kept on B, once stopped mid-film (recording runs
	// in the background).
	time.Sleep(300 * time.Millisecond)
	if got, _ := stB.ContinueWatching(ctx, playback.PeerPrefix+x.aOnB.ID, 0, 10); len(got) != 0 {
		t.Fatalf("B kept a position: %v", got)
	}
}

// Un-offering the library, or ending the pairing, ends a paired server's
// playbacks here at once, both sides: A's viewer's stream stops (MSFP
// playback, section 6.3).
func TestPeerPlaybacksEndAtOnce(t *testing.T) {
	ctx := context.Background()
	x := pairForPlay(t)
	play := func() peerSession {
		status, body := post(t, x.srvA, "/api/v1/playback", `{"titleId":"title_movie_tmdb_949","capabilities":{}}`)
		var s peerSession
		json.Unmarshal([]byte(body), &s)
		if status != 201 {
			t.Fatalf("play: %d %s", status, body)
		}
		if code, _ := viewerGet(t, x.srvA, s.Stream.URL); code != 200 {
			t.Fatalf("the stream: %d", code)
		}
		return s
	}
	s := play()
	if err := x.b.SetOffers(ctx, x.aOnB.ID, nil); err != nil {
		t.Fatal(err)
	}
	// The engine refuses the ticket at once, and tells B's playback to
	// stop, in the background.
	deadline := time.Now().Add(5 * time.Second)
	for len(livePlaybacks(x.onB)) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the library no longer offered: B's playback still live")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code, _ := viewerGet(t, x.srvA, s.Stream.URL); code != http.StatusNotFound {
		t.Fatalf("the library no longer offered: the stream through A %d", code)
	}
	if err := x.b.SetOffers(ctx, x.aOnB.ID, []string{"films"}); err != nil {
		t.Fatal(err)
	}
	s = play()
	if err := x.a.End(ctx, x.bOnA.ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := viewerGet(t, x.srvA, s.Stream.URL); code != http.StatusNotFound {
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

// B refusing a playback tells A why, and A its viewer: a title B can't
// play isn't playable, B's limit for A reached is busy.
func TestPeerRefusalsToldApart(t *testing.T) {
	ctx := context.Background()
	x := pairForPlay(t)
	start := func() (int, string) {
		status, body := post(t, x.srvA, "/api/v1/playback", `{"titleId":"title_movie_tmdb_949","capabilities":{}}`)
		var p struct{ Type string }
		json.Unmarshal([]byte(body), &p)
		return status, p.Type
	}
	x.fakeB.mu.Lock()
	x.fakeB.notPlayable = true
	x.fakeB.mu.Unlock()
	if status, typ := start(); status != http.StatusConflict || typ != "not-playable" {
		t.Fatalf("a title B can't play: %d %s", status, typ)
	}
	x.fakeB.mu.Lock()
	x.fakeB.notPlayable = false
	x.fakeB.mu.Unlock()
	if err := x.b.SetLimits(ctx, x.aOnB.ID, msfp.Limits{}); err != nil {
		t.Fatal(err)
	}
	if status, typ := start(); status != http.StatusServiceUnavailable || typ != "server-busy" {
		t.Fatalf("B's limit for A reached: %d %s", status, typ)
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
	status, body := post(t, x.srvA, "/api/v1/playback", `{"titleId":"title_movie_tmdb_949","capabilities":{}}`)
	var p struct{ Type string }
	_ = json.Unmarshal([]byte(body), &p)
	if status != http.StatusServiceUnavailable || p.Type != "server-busy" {
		t.Fatalf("an unsigned 429 from B's host: %d %s", status, p.Type)
	}
}

// B says it can't be called (MSFP core section 7.5): A connects to it no
// more, its viewers' playback from B among them, which ends at once.
func TestRemotePlaybackEndsWhenPeerUncallable(t *testing.T) {
	ctx := context.Background()
	x := pairForPlay(t)
	status, body := post(t, x.srvA, "/api/v1/playback", `{"titleId":"title_movie_tmdb_949","capabilities":{}}`)
	var s peerSession
	json.Unmarshal([]byte(body), &s)
	if status != 201 {
		t.Fatalf("play: %d %s", status, body)
	}
	if code, _ := viewerGet(t, x.srvA, s.Stream.URL); code != 200 {
		t.Fatalf("the stream through A: %d", code)
	}
	if err := x.b.SetUncallable(ctx, true); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		p, _ := x.a.Peer(ctx, x.bOnA.ID)
		if p.Inbound == msfp.InboundNo {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("B still callable on A: %+v", p)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code, _ := viewerGet(t, x.srvA, s.Stream.URL); code != http.StatusNotFound {
		t.Fatalf("A still relays B's stream, B saying it can't be called: %d", code)
	}
}

// playPeer starts A's viewer's playback of B's film, and fetches its
// stream once.
func (x *twoServers) playPeer(t *testing.T) peerSession {
	t.Helper()
	status, body := post(t, x.srvA, "/api/v1/playback", `{"titleId":"title_movie_tmdb_949","capabilities":{}}`)
	var s peerSession
	json.Unmarshal([]byte(body), &s)
	if status != 201 {
		t.Fatalf("play: %d %s", status, body)
	}
	if code, _ := viewerGet(t, x.srvA, s.Stream.URL); code != 200 {
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

// A's viewer's progress reaches B at the first report, and a pause at
// once: B's live view (the control panel's Now playing) shows where the
// paired server's viewer is.
func TestPeerProgressShownByOwner(t *testing.T) {
	x := pairForPlay(t)
	s := x.playPeer(t)
	onB := func(positionMs int64, paused bool) func() bool {
		return func() bool {
			live := x.onB.Live(time.Now())
			return len(live) == 1 && live[0].PositionMs == positionMs && live[0].Paused == paused
		}
	}
	if status, body := post(t, x.srvA, "/api/v1/playback/"+s.ID+"/progress", `{"positionMs":600000}`); status != 204 {
		t.Fatalf("progress: %d %s", status, body)
	}
	waitFor(t, "B's live view after the first report", onB(600_000, false))
	if status, _ := post(t, x.srvA, "/api/v1/playback/"+s.ID+"/progress", `{"positionMs":610000,"paused":true}`); status != 204 {
		t.Fatal("progress")
	}
	waitFor(t, "B's live view after a pause", onB(610_000, true))
	if status, _ := post(t, x.srvA, "/api/v1/playback/"+s.ID+"/progress", `{"positionMs":610000}`); status != 204 {
		t.Fatal("progress")
	}
	waitFor(t, "B's live view after the resume", onB(610_000, false))
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
