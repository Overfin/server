package federation

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/msfp/wire"
)

// moveTo is srv restarted with another site address, as its operator
// changing it does: a federation built anew for address, on the same
// storage and keys, served at srv's test server and, with listen, at a
// second one, whose address it then is (address is ignored). Its address
// noted, the move begins (NoteAddress). Every test server it had serves
// the new federation (one handler, routes, for all).
func moveTo(t *testing.T, srv *server, address string, listen bool) *server {
	t.Helper()
	mux := http.NewServeMux()
	if listen {
		ts := httptest.NewTLSServer(srv.routes)
		t.Cleanup(ts.Close)
		address = ts.URL
	}
	old := srv.s
	s := &Service{Store: old.Store, Keys: old.Keys, Address: address, HTTP: old.HTTP, Name: old.Name,
		Logger: old.Logger, Seen: old.Seen, Certificates: old.Certificates, Limits: old.Limits, Now: old.Now}
	reg := plugin.NewRegistrar(mux, func(h http.Handler) http.Handler { return h })
	if err := reg.Load(Plugin{S: s}); err != nil {
		t.Fatal(err)
	}
	srv.routes.set(mux) // every address it had serves the new federation
	if err := s.NoteAddress(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &server{s: s, url: address, db: srv.db, routes: srv.routes, site: srv.site}
}

// waitFor polls until ok, for the work done in the background.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for range 200 {
		if ok() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("never: %s", what)
}

func peerOn(t *testing.T, s *server, id string) *store.Peer {
	t.Helper()
	p, err := s.s.Store.PeerByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// signedFor is the address s takes from's request, signed for at's host,
// as signed for (PeerCall.Address): what a play answer's URLs are on.
func signedFor(t *testing.T, s, from *server, at string) string {
	t.Helper()
	body := []byte(`{}`)
	req := httptest.NewRequest("POST", at+playPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	wire.SignRequest(req, body, from.s.Keys.Current(), from.s.now())
	var got string
	SignRefusals(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, ok := s.s.PeerRequest(w, r); ok {
			got = c.Address
		}
	})).ServeHTTP(httptest.NewRecorder(), req)
	if got == "" {
		t.Fatalf("a request signed for %s refused", at)
	}
	return got
}

// B's address changes (MSFP core section 13): A is told in a hello, calls
// hello at the new address, which B answers under its key, and stores it;
// B, once A's requests are signed for its new host, ends the move, and its
// former host is refused. Then B moves back, and A follows again.
func TestAddressChange(t *testing.T) {
	ctx := context.Background()
	a, b, bOnA, aOnB := pairedPair(t)
	for _, s := range []*server{a, b} {
		if err := s.s.NoteAddress(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// A's requests so far were signed for B's only address.
	if _, err := a.s.Check(ctx, bOnA); err != nil {
		t.Fatal(err)
	}
	before := b.url
	b2 := moveTo(t, b, "", true)
	if former, _, ok := b2.s.Moving(); !ok || former != before {
		t.Fatalf("B's move: %q, %v", former, ok)
	}
	waitFor(t, "A stores B's new address", func() bool { return peerOn(t, a, bOnA.ID).Address == b2.url })
	moved := peerOn(t, a, bOnA.ID)
	if moved.Move != nil || len(moved.Certificates) == 0 {
		t.Fatalf("A's view of B, moved: move %+v, pins %v", moved.Move, moved.Certificates)
	}
	waitFor(t, "B ends its move", func() bool { _, _, ok := b2.s.Moving(); return !ok })
	if got := peerOn(t, b2, aOnB.ID); got.OurAddress != b2.url {
		t.Fatalf("B's view of A: calls %q", got.OurAddress)
	}
	// A calls the new address, and the catalog works there.
	if _, err := a.s.Check(ctx, moved); err != nil {
		t.Fatalf("a hello at the new address: %v", err)
	}
	if _, err := a.s.TheirLibraries(ctx, moved); err != nil {
		t.Fatalf("libraries at the new address: %v", err)
	}
	// The former host: no longer accepted, now that every peer moved.
	if _, err := a.s.Check(ctx, bOnA); err == nil {
		t.Fatal("a request signed for B's former host accepted after the move")
	}

	// And back.
	b3 := moveTo(t, b2, before, false)
	if former, _, ok := b3.s.Moving(); !ok || former != b2.url {
		t.Fatalf("B's move back: %q, %v", former, ok)
	}
	waitFor(t, "A stores B's address again", func() bool { return peerOn(t, a, bOnA.ID).Address == before })
	waitFor(t, "B ends its move back", func() bool { _, _, ok := b3.s.Moving(); return !ok })
	if _, err := a.s.TheirLibraries(ctx, peerOn(t, a, bOnA.ID)); err != nil {
		t.Fatalf("libraries after moving back: %v", err)
	}
}

// B's hello names an address A must not connect to (link-local, where a
// cloud's metadata service answers: MSFP core section 3.3): A never
// connects, keeps calling B's old address, which B still accepts during
// its move, and notes the failure for its operator. B itself, not
// reaching itself there either (its own outbound rules), names its
// former address meanwhile: A learns of the new one as a hello from
// someone else naming it would tell it.
func TestAddressChangeRefusedDestination(t *testing.T) {
	ctx := context.Background()
	a, b, bOnA, aOnB := pairedPair(t)
	for _, s := range []*server{a, b} {
		if err := s.s.NoteAddress(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.s.Check(ctx, bOnA); err != nil {
		t.Fatal(err)
	}
	b2 := moveTo(t, b, "https://169.254.169.254", false)
	if !b2.s.Waiting() || b2.s.named() != b.url {
		t.Fatalf("B, its new address refused: waiting %v, names %q", b2.s.Waiting(), b2.s.named())
	}
	exec(t, a, `UPDATE peers SET new_address = ? WHERE id = ?`, b2.url, bOnA.ID)
	peers, err := a.s.Store.Peers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a.s.maintainAddresses(ctx, peers)
	p := peerOn(t, a, bOnA.ID)
	if p.Address != b.url || p.Move == nil || p.Move.Address != b2.url || p.Move.Failing.IsZero() {
		t.Fatalf("A's view of B: %q, move %+v", p.Address, p.Move)
	}
	// The old address still works: B accepts its former host.
	if _, err := a.s.TheirLibraries(ctx, p); err != nil {
		t.Fatalf("libraries at the old address: %v", err)
	}
	onB := peerOn(t, b2, aOnB.ID)
	if notMoved, _ := b2.s.NotMoved(onB); !notMoved {
		t.Fatal("B counts A as moved")
	}
	// B's play answers to A name the address A signed for.
	if got := signedFor(t, b2, a, b.url); got != b.url {
		t.Fatalf("B's URLs for A on %q", got)
	}
	// A new try waits an hour.
	if p.Move.Due(time.Now()) || !p.Move.Due(time.Now().Add(time.Hour)) {
		t.Fatalf("retry: %+v", p.Move)
	}
}

// B's hello names an address where another server answers (C, under its
// own key): the hello there isn't answered under B's key, so A keeps B's
// old address; and once B names its stored address again, the move is
// dropped, and B, whose peers all call that address, has no move to wait
// for. B itself, getting C's answer there, never names that address.
func TestAddressChangeNotConfirmed(t *testing.T) {
	ctx := context.Background()
	a, b, bOnA, _ := pairedPair(t)
	c := newServer(t, "C")
	for _, s := range []*server{a, b} {
		if err := s.s.NoteAddress(ctx); err != nil {
			t.Fatal(err)
		}
	}
	b2 := moveTo(t, b, c.url, false)
	if err := b2.s.answersAt(ctx, c.url); err == nil {
		t.Fatal("B recognised C's answer as its own")
	}
	if !b2.s.Waiting() || b2.s.named() != b.url {
		t.Fatalf("B, another server at its new address: waiting %v, names %q", b2.s.Waiting(), b2.s.named())
	}
	exec(t, a, `UPDATE peers SET new_address = ? WHERE id = ?`, c.url, bOnA.ID)
	peers, err := a.s.Store.Peers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a.s.maintainAddresses(ctx, peers)
	if p := peerOn(t, a, bOnA.ID); p.Address != b.url || p.Move == nil || p.Move.Address != c.url || p.Move.Failing.IsZero() {
		t.Fatalf("A's view of B: %q, move %+v", p.Address, p.Move)
	}
	// B goes back to its address before the move was confirmed: its
	// hello names the stored one, and the move is dropped.
	b3 := moveTo(t, b2, b.url, false)
	// A still calls that address: B's move back is over at once.
	if _, _, ok := b3.s.Moving(); ok {
		t.Fatal("a move back to the address every peer calls is under way")
	}
	if _, err := b3.s.Check(ctx, peerOn(t, b3, onlyPeer(t, b3).ID)); err != nil {
		t.Fatal(err)
	}
	if p := peerOn(t, a, bOnA.ID); p.Address != b.url || p.Move != nil {
		t.Fatalf("A's view of B after: %q, move %+v", p.Address, p.Move)
	}
}

// B's new address doesn't reach it yet (MSFP core section 13.1, step 2):
// B names its former address in its hellos, so A isn't told, until B's
// own request there is answered under B's key; then A is told at once,
// and moves.
func TestAddressAnnouncedOnceItAnswers(t *testing.T) {
	ctx := context.Background()
	a, b, bOnA, aOnB := pairedPair(t)
	for _, s := range []*server{a, b} {
		if err := s.s.NoteAddress(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// B's new address: nothing behind it at first (a proxy not set up
	// yet), then B.
	var up atomic.Bool
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			http.Error(w, "no route", http.StatusBadGateway)
			return
		}
		b.routes.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	b2 := moveTo(t, b, ts.URL, false)
	time.Sleep(200 * time.Millisecond) // several tries
	if !b2.s.Waiting() {
		t.Fatal("B announces an address that doesn't answer")
	}
	// A hello from B, meanwhile, names the former address.
	if _, err := b2.s.Check(ctx, peerOn(t, b2, aOnB.ID)); err != nil {
		t.Fatal(err)
	}
	if p := peerOn(t, a, bOnA.ID); p.Address != b.url || p.Move != nil {
		t.Fatalf("A told of an address that doesn't answer: %q, move %+v", p.Address, p.Move)
	}
	up.Store(true)
	waitFor(t, "A stores B's new address", func() bool { return peerOn(t, a, bOnA.ID).Address == ts.URL })
	if b2.s.Waiting() || b2.s.named() != ts.URL {
		t.Fatalf("B after: waiting %v, names %q", b2.s.Waiting(), b2.s.named())
	}
	// A now signs for the new host, and B's play answers are on it.
	if got := signedFor(t, b2, a, ts.URL); got != ts.URL {
		t.Fatalf("B's URLs for A on %q", got)
	}
}

// A peer whose key was revoked hears nothing from that key but a rotation
// (MSFP core section 12.6), and no call goes to it: an address it named
// before it was suspended isn't confirmed by a hello there, which it
// would answer under the revoked key.
func TestSuspendedPeerMoveNotConfirmed(t *testing.T) {
	ctx := context.Background()
	a, b, bOnA, aOnB := pairedPair(t)
	for _, s := range []*server{a, b} {
		if err := s.s.NoteAddress(ctx); err != nil {
			t.Fatal(err)
		}
	}
	key := b.s.Keys.Current()
	rev := wire.SignRevocation(key, b.s.now())
	if status, _ := signedCall(t, b, peer(t, b, aOnB.ID), revokePath, marshal(t, rev.Body()), key); status != 200 {
		t.Fatalf("revoke: %d", status)
	}
	// B, still holding the revoked key, answers at another address.
	before := b.url
	b2 := moveTo(t, b, "", true)
	exec(t, a, `UPDATE peers SET new_address = ? WHERE id = ?`, b2.url, bOnA.ID)
	got := peer(t, a, bOnA.ID)
	if got.Suspended == nil || got.Move == nil {
		t.Fatalf("A's view of B: suspended %v, move %+v", got.Suspended, got.Move)
	}
	peers, err := a.s.Store.Peers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a.s.maintainAddresses(ctx, peers)
	if p := peer(t, a, bOnA.ID); p.Address != before {
		t.Fatalf("a suspended peer's address moved, under its revoked key, to %q", p.Address)
	}
}
