package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/federation"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

// viewer is another server, A, paired with the test server, B, whose
// films' library B offers it.
func viewer(t *testing.T, b *federation.Service, client *http.Client) (*federation.Service, *store.Peer, *store.Peer) {
	t.Helper()
	ctx := context.Background()
	box, _ := seal.New(bytes.Repeat([]byte{4}, 32))
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "a.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key, _ := federation.LoadKey(ctx, st)
	a := &federation.Service{Store: st, Key: key, Address: "https://a.example.org", HTTP: client,
		Name: func(context.Context) string { return "A" }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	code, _, err := b.NewCode(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	bOnA, err := a.UseCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := b.Store.Peers(ctx)
	aOnB, _ := b.Accept(ctx, list[0].ID) // A can't be reached: paired anyway
	if err := b.SetOffers(ctx, aOnB, []string{"films"}); err != nil {
		t.Fatal(err)
	}
	return a, bOnA, aOnB
}

type peerSession struct {
	ID     string
	Method string
	Stream struct{ URL string }
}

// A's viewer plays B's film straight from B: the stream answers A's site
// with the ticket alone, nothing personal is kept on B, the pairing's
// limits hold, a title B doesn't offer A isn't played, and stopping ends
// the stream.
func TestPeerPlayback(t *testing.T) {
	ctx := context.Background()
	var st *store.Store
	var b *federation.Service
	srv, fake := newServerWith(t, options{store: &st, federation: &b})
	fake.setDirect(true)
	st.PutCatalog(ctx, []store.CatalogItem{
		{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "949", LibraryID: "films", AudioTags: []string{"fr"}},
		{ID: fmt.Sprintf("%032x", 2), Kind: "movie", Name: "Secret", SortName: "secret", TMDB: "1", LibraryID: "private"},
	}, 1)
	st.RefreshTitleKeys(ctx)
	st.SetLibraries(ctx, []store.Library{{ID: "films", Item: []byte(`{"Name":"Films"}`)}, {ID: "private", Item: []byte(`{"Name":"Privé"}`)}})
	a, bOnA, aOnB := viewer(t, b, srv.Client())

	var s peerSession
	status, err := a.Call(ctx, bOnA, "/api/v1/federation/play", map[string]any{"title": "movie/tmdb/949", "capabilities": map[string]any{},
		"audioLanguage": "fr-CA"}, &s)
	if err != nil || status != 200 || !strings.HasPrefix(s.Stream.URL, srv.URL+"/api/v1/stream/") {
		t.Fatalf("play: %d %v %+v", status, err, s)
	}
	// A, for its viewer: no cookie, the ticket alone; no CORS, since no
	// browser ever asks here (rule 97).
	fetch := func() *http.Response {
		req, _ := http.NewRequest("GET", s.Stream.URL, nil)
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := fetch()
	io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("stream: %d, allowed origin %q", res.StatusCode, res.Header.Get("Access-Control-Allow-Origin"))
	}
	// A browser with the ticket gets nothing, from any site.
	for _, h := range []map[string]string{{"Origin": "https://a.example.org"}, {"Sec-Fetch-Site": "cross-site"}, {"Sec-Fetch-Mode": "no-cors"}} {
		req, _ := http.NewRequest("GET", s.Stream.URL, nil)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		if res, err := srv.Client().Do(req); err != nil || res.StatusCode != http.StatusNotFound {
			t.Fatalf("a browser (%v): %v %v", h, res, err)
		}
	}
	// One stream for this pairing: a second is refused.
	st.SetPeerLimits(ctx, aOnB.ID, store.PeerLimits{Streams: 1, Transcodes: 1})
	var s2 peerSession
	if status, _ := a.Call(ctx, bOnA, "/api/v1/federation/play", map[string]any{"title": "movie/tmdb/949", "capabilities": map[string]any{}}, &s2); status != http.StatusConflict {
		t.Fatalf("over the pairing's limit: %d", status)
	}
	// A title in a library B doesn't offer A.
	if status, _ := a.Call(ctx, bOnA, "/api/v1/federation/play", map[string]any{"title": "movie/tmdb/1", "capabilities": map[string]any{}}, &s2); status != http.StatusNotFound {
		t.Fatalf("a title not offered: %d", status)
	}
	// Still watching, paused: noted; a playback that isn't A's is refused,
	// signed.
	if status, err := a.Call(ctx, bOnA, "/api/v1/federation/progress", map[string]any{"id": s.ID, "positionMs": 300000, "paused": true}, &struct{}{}); status != 200 || err != nil {
		t.Fatalf("progress: %d %v", status, err)
	}
	if status, err := a.Call(ctx, bOnA, "/api/v1/federation/progress", map[string]any{"id": "play_" + strings.Repeat("a", 26), "positionMs": 0}, &struct{}{}); status != http.StatusGone || err != nil {
		t.Fatalf("progress of another playback: %d %v", status, err)
	}
	if status, err := a.Call(ctx, bOnA, "/api/v1/federation/stop", map[string]any{"id": s.ID, "positionMs": 600000}, &struct{}{}); status != 200 || err != nil {
		t.Fatalf("stop: %d %v", status, err)
	}
	if res := fetch(); res.StatusCode != http.StatusNotFound {
		res.Body.Close()
		t.Fatalf("the stream after stopping: %d", res.StatusCode)
	}
	// Nothing personal kept here, once stopped mid-film (recording runs in
	// the background).
	time.Sleep(300 * time.Millisecond)
	if got, _ := st.ContinueWatching(ctx, "peer:"+aOnB.ID, 0, 10); len(got) != 0 {
		t.Fatalf("B kept a position: %v", got)
	}
}

// twoServers is B, holding a film, Heat, paired with A, which wants B's
// films and knows of Heat.
type twoServers struct {
	srvA, srvB *httptest.Server
	fakeB      *fakeJellyfin
	stA, stB   *store.Store
	a, b       *federation.Service
	onB        *playback.Tickets
	aOnB, bOnA *store.Peer
}

// pairForPlay makes twoServers. With trustB, A trusts B's test
// certificate as if a public authority had issued it; without, A
// accepts it only as the one B names.
func pairForPlay(t *testing.T, trustB bool) *twoServers {
	t.Helper()
	return pairForPlayWith(t, trustB, nil)
}

// pairForPlayWith is pairForPlay with B's handler wrapped by wrapB.
func pairForPlayWith(t *testing.T, trustB bool, wrapB func(http.Handler) http.Handler) *twoServers {
	t.Helper()
	ctx := context.Background()
	var x twoServers
	x.srvB, x.fakeB = newServerWith(t, options{store: &x.stB, federation: &x.b, tickets: &x.onB, wrap: wrapB})
	x.fakeB.setDirect(true)
	x.stB.PutCatalog(ctx, []store.CatalogItem{{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "Heat", SortName: "heat",
		TMDB: "949", LibraryID: "films", AudioTags: []string{"fr"}}}, 1)
	x.stB.RefreshTitleKeys(ctx)
	x.stB.SetLibraries(ctx, []store.Library{{ID: "films", Item: []byte(`{"Name":"Films"}`)}})
	x.srvA, _ = newServerWith(t, options{store: &x.stA, federation: &x.a})
	if trustB {
		x.a.HTTP = x.srvB.Client()
	} else {
		x.b.Certificates = &federation.Certificates{}
	}

	code, _, _ := x.b.NewCode(ctx, "")
	bOnA, err := x.a.UseCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := x.stB.Peers(ctx)
	x.aOnB, _ = x.b.Accept(ctx, list[0].ID)
	x.stA.SetPeerState(ctx, bOnA.ID, store.PeerTheirs, store.PeerPaired)
	x.bOnA, _ = x.stA.PeerByID(ctx, bOnA.ID)
	x.b.SetOffers(ctx, x.aOnB, []string{"films"})
	if err := x.a.SetWants(ctx, x.bOnA, []string{"films"}); err != nil {
		t.Fatal(err)
	}
	if n, err := x.a.SyncTitles(ctx, x.bOnA); err != nil || n != 1 {
		t.Fatalf("sync: %d %v", n, err)
	}
	x.stA.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "949", Name: "Heat", Year: 1995}, []byte(`{}`), time.Now())
	return &x
}

// Someone on A plays a film only B has: A asks B, the stream comes
// through A (the browser never asks B), A keeps where they are, and
// stopping on A ends it on B.
func TestPlayFromPeer(t *testing.T) {
	ctx := context.Background()
	x := pairForPlay(t, true)
	srvA, srvB, stB, onB, aOnB := x.srvA, x.srvB, x.stB, x.onB, x.aOnB

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
	// The viewer's browser asks A, which asks B.
	viewer := func(path string) (int, string) {
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
	if code, got := viewer(s.Stream.URL); code != 200 || got == "" {
		t.Fatalf("the stream through A: %d %q", code, got)
	}
	// B's ticket stays between the servers.
	onBNow := onB.Where(func(*playback.Stream) bool { return true })
	if len(onBNow) != 1 {
		t.Fatalf("playbacks on B: %d", len(onBNow))
	}
	bURL := srvB.URL + "/api/v1/stream/" + onBNow[0].Ticket + "/file"
	if strings.Contains(body, onBNow[0].Ticket) {
		t.Fatal("the viewer got B's ticket")
	}
	if status, body := post(t, srvA, "/api/v1/playback/"+s.ID+"/progress", `{"positionMs":600000}`); status != 204 {
		t.Fatalf("progress: %d %s", status, body)
	}
	// Another audio track, with one stream for the pairing: the browser's
	// start comes before its stop of the old playback, which ends first.
	stB.SetPeerLimits(ctx, aOnB.ID, store.PeerLimits{Streams: 1, Transcodes: 1})
	status, body = post(t, srvA, "/api/v1/playback", `{"titleId":"title_movie_tmdb_949","capabilities":{},"audioLanguage":"fr"}`)
	var restarted peerSession
	json.Unmarshal([]byte(body), &restarted)
	if status != 201 {
		t.Fatalf("a restart under a 1-stream limit: %d %s", status, body)
	}
	if code, _ := viewer(s.Stream.URL); code != http.StatusNotFound {
		t.Errorf("the old playback's stream after the restart: %d", code)
	}
	if n := len(onB.Where(func(*playback.Stream) bool { return true })); n != 1 {
		t.Errorf("playbacks on B after the restart: %d", n)
	}
	if status, _ := post(t, srvA, "/api/v1/playback/"+s.ID+"/stop", `{"positionMs":600000}`); status != 204 {
		t.Fatalf("stop: %d", status)
	}
	// The playback started again, then B ends the pairing: its stream ends
	// at once.
	again := restarted
	if err := stB.DeletePeer(ctx, aOnB.ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := viewer(again.Stream.URL); code != http.StatusNotFound {
		t.Fatalf("after the pairing ended: %d", code)
	}
	// B hears of the first one's stop in the background.
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, err := srvB.Client().Get(bURL)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode == http.StatusNotFound {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("B's stream after A stopped: %d", res.StatusCode)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// A's site lists what B holds for it, beside its own: the library taken
// from B is an entry of the menu, "Films (B)" (or A's own name for it),
// listing B's films; all titles list both films, each once.
func TestPeerTitlesListed(t *testing.T) {
	ctx := context.Background()
	var stB, stA *store.Store
	var b, a *federation.Service
	srvB, _ := newServerWith(t, options{store: &stB, federation: &b})
	b.Name = func(context.Context) string { return "Bee" }
	stB.PutCatalog(ctx, []store.CatalogItem{{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "Heat", SortName: "heat",
		TMDB: "949", LibraryID: "films"}, {ID: fmt.Sprintf("%032x", 2), Kind: "movie", Name: "Up", SortName: "up",
		TMDB: "14160", LibraryID: "films"}}, 1)
	stB.RefreshTitleKeys(ctx)
	stB.SetLibraries(ctx, []store.Library{{ID: "films", Item: []byte(`{"Name":"Films","CollectionType":"movies"}`)}})
	srvA, _ := newServerWith(t, options{store: &stA, federation: &a})
	a.HTTP = srvB.Client()
	// A has Up itself; it keeps TMDB's pages of both.
	stA.PutCatalog(ctx, []store.CatalogItem{{ID: fmt.Sprintf("%032x", 9), Kind: "movie", Name: "Up", SortName: "up",
		TMDB: "14160", LibraryID: "mine"}}, 1)
	stA.RefreshTitleKeys(ctx)
	stA.SetCatalogState(ctx, store.CatalogState{Pass: 1})
	now := time.Now()
	stA.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "949", Name: "Heat", Year: 1995}, []byte(`{}`), now)
	stA.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "14160", Name: "Up", Year: 2009}, []byte(`{}`), now)

	code, _, _ := b.NewCode(ctx, "")
	bOnA, err := a.UseCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := stB.Peers(ctx)
	aOnB, _ := b.Accept(ctx, list[0].ID)
	stA.SetPeerState(ctx, bOnA.ID, store.PeerTheirs, store.PeerPaired)
	bOnA, _ = stA.PeerByID(ctx, bOnA.ID)
	b.SetOffers(ctx, aOnB, []string{"films"})
	if err := a.SetWants(ctx, bOnA, []string{"films"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SyncTitles(ctx, bOnA); err != nil {
		t.Fatal(err)
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
	if err := stA.LabelPeerWant(ctx, bOnA.ID, "films", "Their films"); err != nil {
		t.Fatal(err)
	}
	get(t, srvA, "/api/v1/libraries", &libs)
	if !slices.ContainsFunc(libs.Items, func(l struct{ ID, Name, Kind string }) bool { return l.Name == "Their films" && l.ID == entry }) {
		t.Fatalf("renamed: %+v", libs.Items)
	}
}

// B refusing a playback tells A why, and A its viewer: a title B can't
// play isn't playable, B's limit for A reached is busy; a 409 without a
// type (another implementation's) is busy too.
func TestPeerRefusalsToldApart(t *testing.T) {
	ctx := context.Background()
	x := pairForPlay(t, true)
	start := func() (int, string) {
		status, body := post(t, x.srvA, "/api/v1/playback", `{"titleId":"title_movie_tmdb_949","capabilities":{}}`)
		var p struct{ Type string }
		json.Unmarshal([]byte(body), &p)
		return status, p.Type
	}
	x.fakeB.mu.Lock()
	x.fakeB.notPlayable = true
	x.fakeB.mu.Unlock()
	if r, err := x.a.CallReply(ctx, x.bOnA, "/api/v1/federation/play", map[string]any{"title": "movie/tmdb/949"}, &struct{}{}); err != nil ||
		r.Status != http.StatusConflict || r.Problem != "not-playable" {
		t.Fatalf("B's refusal: %+v %v", r, err)
	}
	if status, typ := start(); status != http.StatusConflict || typ != "not-playable" {
		t.Fatalf("a title B can't play: %d %s", status, typ)
	}
	x.fakeB.mu.Lock()
	x.fakeB.notPlayable = false
	x.fakeB.mu.Unlock()
	x.stB.SetPeerLimits(ctx, x.aOnB.ID, store.PeerLimits{})
	if status, typ := start(); status != http.StatusServiceUnavailable || typ != "server-busy" {
		t.Fatalf("B's limit for A reached: %d %s", status, typ)
	}
}

// B's certificate isn't publicly trusted: A pairs with it all the same,
// learns it from B's hello, and plays B's film through it; once B names
// it no longer, A neither starts a playback nor fetches the media of one
// started before.
func TestPlayFromPeerOverNamedCertificate(t *testing.T) {
	ctx := context.Background()
	x := pairForPlay(t, false)
	if bOnA, _ := x.stA.PeerByID(ctx, x.bOnA.ID); len(bOnA.Certificates) != 1 {
		t.Fatalf("A's view of B's certificates: %v", bOnA.Certificates)
	}
	status, body := post(t, x.srvA, "/api/v1/playback", `{"titleId":"title_movie_tmdb_949","capabilities":{}}`)
	var s peerSession
	json.Unmarshal([]byte(body), &s)
	if status != 201 {
		t.Fatalf("play on A: %d %s", status, body)
	}
	viewer := func(path string) int {
		req, _ := http.NewRequest("GET", x.srvA.URL+path, nil)
		req.Header.Set("Remote-User", subject)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		res, err := client(x.srvA, subject).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		res.Body.Close()
		return res.StatusCode
	}
	if code := viewer(s.Stream.URL); code != 200 {
		t.Fatalf("the stream through A: %d", code)
	}
	// B names no certificate any more (or someone else's stands in its
	// place): the media is refused, after asking B's hello again.
	x.b.Certificates = nil
	x.stA.SetPeerCertificates(ctx, x.bOnA.ID, nil)
	if code := viewer(s.Stream.URL); code == 200 {
		t.Fatalf("the stream over a certificate B no longer names: %d", code)
	}
	if status, body := post(t, x.srvA, "/api/v1/playback", `{"titleId":"title_movie_tmdb_949","capabilities":{}}`); status == 201 {
		t.Fatalf("a playback over a certificate B no longer names: %d %s", status, body)
	}
}
