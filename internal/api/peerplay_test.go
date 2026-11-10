package api_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/federation"
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
	// The viewer's browser, from A's site: no cookie, the ticket alone.
	fetch := func() *http.Response {
		req, _ := http.NewRequest("GET", s.Stream.URL, nil)
		req.Header.Set("Origin", "https://a.example.org")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := fetch()
	io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Access-Control-Allow-Origin") != "https://a.example.org" {
		t.Fatalf("stream: %d, allowed origin %q", res.StatusCode, res.Header.Get("Access-Control-Allow-Origin"))
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
