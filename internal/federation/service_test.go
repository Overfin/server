package federation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

// server is one Overfin's federation, on a test server of its own.
type server struct {
	s   *Service
	url string
}

func newServer(t *testing.T, name string) *server {
	t.Helper()
	box, _ := seal.New(bytes.Repeat([]byte{9}, 32))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key, err := LoadKey(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	s := &Service{Store: st, Key: key, Address: ts.URL, HTTP: NewHTTPClient(),
		Name:   func(context.Context) string { return name },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	reg := plugin.NewRegistrar(mux, func(h http.Handler) http.Handler { return h })
	if err := reg.Load(Plugin{S: s}); err != nil {
		t.Fatal(err)
	}
	return &server{s: s, url: ts.URL}
}

func onlyPeer(t *testing.T, s *server) *store.Peer {
	t.Helper()
	list, err := s.s.Store.Peers(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("peers: %+v, %v", list, err)
	}
	return list[0]
}

func noPeers(t *testing.T, s *server) {
	t.Helper()
	if list, _ := s.s.Store.Peers(context.Background()); len(list) != 0 {
		t.Fatalf("peers left: %+v", list)
	}
}

// A pairs with B: A makes a code, B uses it, both wait for A's operator,
// who accepts; then B ends it, and both forget each other.
func TestPairing(t *testing.T) {
	ctx := context.Background()
	a, b := newServer(t, "Films de A"), newServer(t, "B")
	code, _, err := a.s.NewCode(ctx, "for B")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.s.UseCode(ctx, "garbage"); !errors.Is(err, ErrCode) {
		t.Fatalf("garbage code: %v", err)
	}
	if _, err := a.s.UseCode(ctx, code); !errors.Is(err, ErrOwnCode) {
		t.Fatalf("own code: %v", err)
	}
	pa, err := b.s.UseCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if pa.State != store.PeerTheirs || pa.Name != "Films de A" || pa.Address != a.url {
		t.Fatalf("B's view of A: %+v", pa)
	}
	pb := onlyPeer(t, a)
	if pb.State != store.PeerOurs || pb.Name != "B" || !SameKey(pb.PublicKey, b.s.Key.Public()) {
		t.Fatalf("A's view of B: %+v", pb)
	}
	// The code is used up: a third server can't use it.
	c := newServer(t, "C")
	if _, err := c.s.UseCode(ctx, code); !errors.Is(err, ErrCodeRefused) {
		t.Fatalf("a used code: %v", err)
	}
	noPeers(t, c)
	// B asks before A's operator accepted: still waiting.
	if got, err := b.s.Check(ctx, pa); err != nil || got.State != store.PeerTheirs {
		t.Fatalf("B's check before accepting: %+v, %v", got, err)
	}
	if got, err := a.s.Accept(ctx, pb.ID); err != nil || got.State != store.PeerPaired {
		t.Fatalf("accepting: %+v, %v", got, err)
	}
	if got := onlyPeer(t, b); got.State != store.PeerPaired {
		t.Fatalf("B wasn't told: %+v", got)
	}
	if _, err := a.s.Accept(ctx, pb.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("accepted twice: %v", err)
	}
	if err := b.s.End(ctx, pa.ID); err != nil {
		t.Fatal(err)
	}
	noPeers(t, b)
	noPeers(t, a)
}

// A's operator refuses B, or A ends the pairing while B is away: B learns
// at its next check that A no longer knows it.
func TestRefused(t *testing.T) {
	ctx := context.Background()
	a, b := newServer(t, "A"), newServer(t, "B")
	code, _, _ := a.s.NewCode(ctx, "")
	pa, err := b.s.UseCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	// Refusing, with B unreachable for a moment: A forgets B anyway.
	b.s.Store.DeletePeer(ctx, pa.ID) // B's copy, so A's "ended" finds nothing
	if err := a.s.End(ctx, onlyPeer(t, a).ID); err != nil {
		t.Fatal(err)
	}
	noPeers(t, a)
	again, _ := b.s.Store.AddPeer(ctx, pa.PublicKey, pa.Address, pa.Name)
	if got, err := b.s.Check(ctx, again); err != nil || got.State != store.PeerGone {
		t.Fatalf("B's check after A refused: %+v, %v", got, err)
	}
}

// Every route answers the same plain 404 to anyone without a pairing.
func TestStrangers(t *testing.T) {
	a := newServer(t, "A")
	stranger := NewKey()
	for _, path := range []string{"/api/v1/federation/hello", "/api/v1/federation/pair"} {
		body := []byte(`{"state":"paired"}`)
		req, _ := http.NewRequest("POST", a.url+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		SignRequest(req, body, stranger, a.s.now())
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound || res.Header.Get(HeaderSignature) != "" {
			t.Errorf("%s: %d, signed %q", path, res.StatusCode, res.Header.Get(HeaderSignature))
		}
		// Unsigned.
		res, err = http.Post(a.url+path, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s unsigned: %d", path, res.StatusCode)
		}
	}
}

// Someone between the two servers can't pass for A: B checks A's answer
// against the key in A's code.
func TestImpostor(t *testing.T) {
	ctx := context.Background()
	a, b, mallory := newServer(t, "A"), newServer(t, "B"), newServer(t, "Mallory")
	code, _, _ := a.s.NewCode(ctx, "")
	c, _ := ParseCode(code)
	// The code, its address changed to Mallory's, who also made a code of
	// her own with the same secret to answer it.
	c.Address = mallory.url
	mallory.s.Store.CreatePairingCode(ctx, c.Secret, "", c.Expires)
	if _, err := b.s.UseCode(ctx, c.String()); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("an answer from another key: %v", err)
	}
	noPeers(t, b)
}

func libraries(t *testing.T, s *server, items ...string) {
	t.Helper()
	var libs []store.Library
	for _, it := range items {
		var l struct{ Id string }
		json.Unmarshal([]byte(it), &l)
		libs = append(libs, store.Library{ID: l.Id, Item: json.RawMessage(it)})
	}
	if err := s.s.Store.SetLibraries(context.Background(), libs); err != nil {
		t.Fatal(err)
	}
}

// B sees only what A offers it, wants only out of that, and nobody else
// sees anything.
func TestSharing(t *testing.T) {
	ctx := context.Background()
	a, b, c := newServer(t, "A"), newServer(t, "B"), newServer(t, "C")
	libraries(t, a, `{"Id":"films","Name":"Films","CollectionType":"movies"}`,
		`{"Id":"series","Name":"<b>Séries</b>","CollectionType":"tvshows"}`, `{"Id":"home","Name":"Vidéos de famille","CollectionType":"homevideos"}`)

	// Before pairing: nothing to share.
	code, _, _ := a.s.NewCode(ctx, "")
	pending, err := b.s.UseCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.s.TheirLibraries(ctx, pending); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("libraries before pairing: %v", err)
	}
	if err := a.s.SetOffers(ctx, onlyPeer(t, a), []string{"films"}); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("offers before pairing: %v", err)
	}
	bOnA, err := a.s.Accept(ctx, onlyPeer(t, a).ID)
	if err != nil {
		t.Fatal(err)
	}
	aOnB, _ := b.s.Store.PeerByID(ctx, pending.ID)

	// Nothing offered yet.
	if got, err := b.s.TheirLibraries(ctx, aOnB); err != nil || len(got) != 0 {
		t.Fatalf("offered before A chose: %v, %v", got, err)
	}
	if err := a.s.SetOffers(ctx, bOnA, []string{"films", "series", "not-a-library"}); err != nil {
		t.Fatal(err)
	}
	a.s.Store.SetPeerLimits(ctx, bOnA.ID, store.PeerLimits{Streams: 3, Transcodes: 1, BitrateKbps: 8000})
	got, err := b.s.TheirLibraries(ctx, aOnB)
	if err != nil || len(got) != 2 || got[0].Name != "Films" || got[1].Name != "Séries" || got[1].Kind != "tvshows" {
		t.Fatalf("B sees: %+v, %v", got, err)
	}
	// B learns what A lets it use.
	if again, _ := b.s.Store.PeerByID(ctx, aOnB.ID); again.Theirs == nil || *again.Theirs != (store.PeerLimits{Streams: 3, Transcodes: 1, BitrateKbps: 8000}) {
		t.Fatalf("B's view of A's limits: %+v", again.Theirs)
	}
	if err := b.s.SetWants(ctx, aOnB, []string{"series", "home"}); err != nil {
		t.Fatal(err)
	}
	if w, _ := b.s.Store.PeerWants(ctx, aOnB.ID); len(w) != 1 || w[0].ID != "series" {
		t.Fatalf("B wants what A doesn't offer: %+v", w)
	}

	// A stranger, signing with its own key, gets the plain 404.
	stranger := &store.Peer{State: store.PeerPaired, Address: a.url, PublicKey: a.s.Key.Public()}
	if _, err := c.s.TheirLibraries(ctx, stranger); !errors.Is(err, ErrUnknown) {
		t.Fatalf("a stranger: %v", err)
	}
}

// B learns which titles the library it wants of A holds, with their
// languages; nothing of a library A doesn't offer it, even if asked.
func TestSyncTitles(t *testing.T) {
	ctx := context.Background()
	a, b := newServer(t, "A"), newServer(t, "B")
	libraries(t, a, `{"Id":"films","Name":"Films","CollectionType":"movies"}`, `{"Id":"private","Name":"Privé","CollectionType":"movies"}`)
	a.s.Store.PutCatalog(ctx, []store.CatalogItem{
		{ID: "00000000000000000000000000000001", Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "949", LibraryID: "films",
			AudioTags: []string{"en", "fr-CA"}, SubtitleTags: []string{"fr"}},
		{ID: "00000000000000000000000000000002", Kind: "movie", Name: "Secret", SortName: "secret", TMDB: "1", LibraryID: "private"},
	}, 1)
	a.s.Store.RefreshTitleKeys(ctx)
	code, _, _ := a.s.NewCode(ctx, "")
	aOnB, _ := b.s.UseCode(ctx, code)
	bOnA, _ := a.s.Accept(ctx, onlyPeer(t, a).ID)
	aOnB, _ = b.s.Store.PeerByID(ctx, aOnB.ID)
	a.s.SetOffers(ctx, bOnA, []string{"films"})
	b.s.SetWants(ctx, aOnB, []string{"films"})
	// Asking for the library A doesn't offer, too: ignored.
	b.s.Store.SetPeerWants(ctx, aOnB.ID, []store.PeerLibrary{{ID: "films", Name: "Films"}, {ID: "private", Name: "Privé"}})
	n, err := b.s.SyncTitles(ctx, aOnB)
	if err != nil || n != 1 {
		t.Fatalf("synced %d, %v", n, err)
	}
	got, _ := b.s.Store.TitlesOfPeer(ctx, aOnB.ID)
	if len(got) != 1 || got[0].Key != "movie/tmdb/949" || len(got[0].Audio) != 2 || got[0].Audio[1] != "fr-CA" || got[0].Subtitles[0] != "fr" {
		t.Fatalf("B knows: %+v", got)
	}
	// B no longer wants anything: it knows of nothing.
	b.s.Store.SetPeerWants(ctx, aOnB.ID, nil)
	if n, _ := b.s.SyncTitles(ctx, aOnB); n != 0 {
		t.Fatalf("still %d", n)
	}
}

func TestCleanTags(t *testing.T) {
	got := cleanTags([]string{"fr-CA", "fr-CA", "", "x", "<script>", "es-419", "toolongtagvalue"})
	if len(got) != 2 || got[0] != "fr-CA" || got[1] != "es-419" {
		t.Fatalf("%v", got)
	}
}
