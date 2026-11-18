package federation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/overfin/server/internal/msfp"
	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/store"
)

// uncallableTo is srv built anew with its operator's setting "can't be
// called" on or off, as the panel does (MSFP core section 7.5): on, under
// its address of that form, made once and kept; off, under its test
// server's again. Its address noted, a move from the one before.
func uncallableTo(t *testing.T, srv *server, on bool) *server {
	t.Helper()
	ctx := context.Background()
	if err := srv.s.NoteAddress(ctx); err != nil { // the address before, if not yet
		t.Fatal(err)
	}
	if err := srv.s.Store.SetFederationUncallable(ctx, on); err != nil {
		t.Fatal(err)
	}
	_, address, err := srv.s.Store.FederationUncallable(ctx, msfp.NewUncallableAddress)
	if err != nil {
		t.Fatal(err)
	}
	if !on {
		address = srv.site
	}
	old := srv.s
	s := &Service{Store: old.Store, Keys: old.Keys, Address: address, Uncallable: on, HTTP: old.HTTP, Name: old.Name,
		Logger: old.Logger, Seen: old.Seen, Certificates: old.Certificates, Limits: old.Limits, Now: old.Now}
	mux := http.NewServeMux()
	reg := plugin.NewRegistrar(mux, func(h http.Handler) http.Handler { return h })
	if err := reg.Load(Plugin{S: s}); err != nil {
		t.Fatal(err)
	}
	srv.routes.set(mux)
	if err := s.NoteAddress(ctx); err != nil {
		t.Fatal(err)
	}
	return &server{s: s, url: address, db: srv.db, routes: srv.routes, site: srv.site}
}

// uncallablePair is A, which can be called, offering its films to B,
// which can't, paired from A's code, B wanting them.
func uncallablePair(t *testing.T) (a, b *server, bOnA, aOnB *store.Peer) {
	t.Helper()
	ctx := context.Background()
	a, b = newServer(t, "A"), uncallableTo(t, newServer(t, "B"), true)
	libraries(t, a, `{"Id":"films","Name":"Films","CollectionType":"movies"}`)
	a.s.Store.PutCatalog(ctx, []store.CatalogItem{{ID: "00000000000000000000000000000001", Kind: "movie", Name: "Heat",
		SortName: "heat", TMDB: "949", LibraryID: "films"}}, 1)
	a.s.Store.RefreshTitleKeys(ctx)
	code, _, err := a.s.NewCode(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := b.s.UseCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if bOnA, err = a.s.Accept(ctx, onlyPeer(t, a).ID); err != nil {
		t.Fatal(err)
	}
	b.s.Hellos(ctx) // its first, at once: B learns A accepted
	aOnB = peer(t, b, pending.ID)
	if aOnB.State != store.PeerPaired {
		t.Fatalf("B's view of A after its hello: %+v", aOnB)
	}
	if err := a.s.SetOffers(ctx, bOnA, []string{"films"}); err != nil {
		t.Fatal(err)
	}
	if err := b.s.SetWants(ctx, aOnB, pub(t, a, "films")); err != nil {
		t.Fatal(err)
	}
	return a, b, peer(t, a, bOnA.ID), peer(t, b, aOnB.ID)
}

// noHits fails when anything connected to s: a server that can't be
// called.
func noHits(t *testing.T, s *server) {
	t.Helper()
	if n := s.routes.hits.Load(); n != 0 {
		t.Fatalf("%d requests reached %s, which can't be called", n, s.url)
	}
}

// B can't be called: it makes no codes, but redeems A's; A never calls
// it, not even on accepting; B learns at its hello, reads A's libraries
// and titles; A reads nothing of B's. A ends the pairing without telling
// B, whose next hello gets the signed 404 of a forgotten key: gone.
func TestUncallablePairing(t *testing.T) {
	ctx := context.Background()
	if _, _, err := uncallableTo(t, newServer(t, "C"), true).s.NewCode(ctx, ""); !errors.Is(err, ErrNoCodes) {
		t.Fatalf("a code from a server that can't be called: %v", err)
	}
	a, b, bOnA, aOnB := uncallablePair(t)
	if err := msfp.CheckUncallable(b.s.Address); err != nil || bOnA.Address != b.s.Address || bOnA.Inbound != store.InboundNo {
		t.Fatalf("A's view of B: %q, %q, %v", bOnA.Address, bOnA.Inbound, err)
	}
	if n, err := b.s.SyncTitles(ctx, aOnB); err != nil || n != 1 {
		t.Fatalf("B reading A's titles: %d, %v", n, err)
	}
	if _, err := a.s.TheirLibraries(ctx, bOnA); !errors.Is(err, ErrUncallable) {
		t.Errorf("A reading B's libraries: %v", err)
	}
	if _, err := a.s.SyncTitles(ctx, bOnA); !errors.Is(err, ErrUncallable) {
		t.Errorf("A reading B's titles: %v", err)
	}
	// Not even where B's test server answers: it can't be called, so
	// nothing is tried. Nor at an address that never resolves, even for
	// a peer said to be callable.
	exec(t, a, `UPDATE peers SET address = ? WHERE id = ?`, b.site, bOnA.ID)
	bOnA = peer(t, a, bOnA.ID)
	for _, path := range []string{helloPath, rotatePath, revokePath, librariesPath, titlesPath, playPath, "/api/v1/federation/stop"} {
		if _, err := a.s.Call(ctx, bOnA, path, struct{}{}, &json.RawMessage{}); !errors.Is(err, ErrUncallable) {
			t.Errorf("A calling %s on B: %v", path, err)
		}
	}
	callable := *bOnA
	callable.Inbound, callable.Address = store.InboundYes, b.s.Address
	if _, err := a.s.Call(ctx, &callable, helloPath, struct{}{}, &json.RawMessage{}); !errors.Is(err, ErrUncallable) {
		t.Errorf("A calling an address that never resolves: %v", err)
	}
	a.s.CheckSoon([]*store.Peer{bOnA})
	a.s.Maintain(ctx)
	a.s.SyncAll(ctx)
	if err := a.s.End(ctx, bOnA.ID); err != nil {
		t.Fatal(err)
	}
	noPeers(t, a)
	b.s.helloNow(aOnB.ID)
	b.s.Hellos(ctx)
	if got := peer(t, b, aOnB.ID); got.State != store.PeerGone {
		t.Fatalf("B's view of A after A ended it: %+v", got)
	}
	noHits(t, b)
}

// inbound false goes with that form of address only (MSFP core sections
// 5.2, 9.1 and 9.2): a pair or a hello request otherwise is a bad body,
// and a hello answer, a badly signed one; nothing is kept of either.
func TestUncallableAddressRequired(t *testing.T) {
	ctx := context.Background()
	a, b := newServer(t, "A"), newServer(t, "B")
	// B says it can't be called, under an address that can be.
	b.s.Uncallable = true
	code, _, _ := a.s.NewCode(ctx, "")
	if _, err := b.s.UseCode(ctx, code); !errors.Is(err, ErrCodeRefused) {
		t.Fatalf("a pair with inbound false at %s: %v", b.url, err)
	}
	noPeers(t, a)
	b.s.Uncallable = false
	pending, err := b.s.UseCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	bOnA, err := a.s.Accept(ctx, onlyPeer(t, a).ID)
	if err != nil {
		t.Fatal(err)
	}
	aOnB := peer(t, b, pending.ID)
	// Its hello: refused, signed, and B still one that can be called.
	for _, body := range []string{
		`{"state":"paired","inbound":false}`,
		`{"state":"paired","inbound":false,"address":"https://b.example.org"}`,
	} {
		if status, _ := signedCall(t, b, aOnB, helloPath, []byte(body), b.s.Keys.Current()); status != http.StatusNotFound {
			t.Errorf("%s: %d", body, status)
		}
	}
	if got := peer(t, a, bOnA.ID); !got.Callable() || got.Address != b.url {
		t.Fatalf("A's view of B: %+v", got)
	}
	// A's answer to B, saying inbound false at A's own address: B keeps
	// nothing of it.
	a.s.Uncallable = true
	before := peer(t, b, aOnB.ID)
	if _, err := b.s.Check(ctx, before); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("B's check: %v", err)
	}
	if got := peer(t, b, aOnB.ID); !got.Callable() || got.Seen.After(*before.Seen) {
		t.Errorf("B's view of A: %+v", got)
	}
}

// The hello schedule of a server that can't be called (MSFP core section
// 7.5), on a test clock: every 20 seconds or so while the other operator
// hasn't accepted, every 5 minutes once paired, every hour once gone,
// each give or take a fifth.
func TestHelloSchedule(t *testing.T) {
	ctx := context.Background()
	a, b := newServer(t, "A"), uncallableTo(t, newServer(t, "B"), true)
	c := sharedClock(a, b)
	b.s.lim().Now, a.s.lim().Now = c.now, c.now
	code, _, _ := a.s.NewCode(ctx, "")
	pending, err := b.s.UseCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	hellos := func() int64 { return a.routes.hits.Load() }
	at := hellos()
	// after: the hellos sent in the time given, one run of Hellos a
	// second, as the gateway does.
	after := func(d time.Duration) int64 {
		t.Helper()
		before := hellos()
		for end := c.now().Add(d); c.now().Before(end); c.move(time.Second) {
			b.s.Hellos(ctx)
		}
		return hellos() - before
	}
	if n := after(time.Second); n != 1 {
		t.Fatalf("at once: %d hellos", n)
	}
	// Waiting: 16 to 24 seconds apart.
	if n := after(15 * time.Second); n != 0 {
		t.Fatalf("within 15 seconds: %d", n)
	}
	if n := after(10 * time.Second); n != 1 {
		t.Fatalf("by 25 seconds: %d", n)
	}
	if n := after(10 * time.Minute); n < 25 || n > 38 {
		t.Fatalf("10 minutes, waiting: %d hellos", n)
	}
	// Paired: every 4 to 6 minutes.
	if _, err := a.s.Accept(ctx, onlyPeer(t, a).ID); err != nil {
		t.Fatal(err)
	}
	// Its next turn, as it was waiting, tells it.
	for i := 0; peer(t, b, pending.ID).State != store.PeerPaired; i++ {
		if i == 30 {
			t.Fatal("B never learnt A accepted")
		}
		after(time.Second)
	}
	if n := after(3*time.Minute + 50*time.Second); n != 0 {
		t.Fatalf("paired, within 3 min 50 s: %d", n)
	}
	if n := after(time.Hour); n < 10 || n > 16 {
		t.Fatalf("paired, an hour: %d hellos", n)
	}
	// Gone (A ended it): every 48 to 72 minutes.
	if err := a.s.End(ctx, onlyPeer(t, a).ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; peer(t, b, pending.ID).State != store.PeerGone; i++ {
		if i == 7*60 {
			t.Fatal("B never learnt A ended it")
		}
		after(time.Second)
	}
	if n := after(47 * time.Minute); n != 0 {
		t.Fatalf("gone, within 47 minutes: %d", n)
	}
	if n := after(5 * time.Hour); n < 4 || n > 7 {
		t.Fatalf("gone, 5 hours: %d hellos", n)
	}
	// A pairing still waiting a day after the pair: every 5 minutes.
	if got := msfp.HelloWait(true, false, 25*time.Hour); got != msfp.HelloPaired {
		t.Errorf("waiting after a day: %v", got)
	}
	if hellos() == at {
		t.Fatal("no hello at all")
	}
	noHits(t, b)
}

// A changes its key, B can't be called (MSFP core section 12.4): no rotate
// to B; the rotation comes in A's answer, B moves (A's commitment), says
// so in its next hello, sent at once, and A, every peer moved, deletes its
// old key.
func TestPulledRotation(t *testing.T) {
	ctx := context.Background()
	a, b, bOnA, aOnB := uncallablePair(t)
	if err := a.s.Rotate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	newKey := a.s.Keys.Current().Public()
	if got := peer(t, a, bOnA.ID); got.OurKey != store.OurKeyOld || got.RotateSent != nil {
		t.Fatalf("A's view of B: %q, rotate sent %v", got.OurKey, got.RotateSent)
	}
	b.s.Hellos(ctx) // its turn is 5 minutes away
	if got := peer(t, b, aOnB.ID); msfp.SameKey(got.PublicKey, newKey) {
		t.Fatal("B moved before its turn")
	}
	b.s.helloNow(aOnB.ID)
	b.s.Hellos(ctx)
	if got := peer(t, b, aOnB.ID); !msfp.SameKey(got.PublicKey, newKey) || got.CommitmentFrom != store.CommitmentRotation {
		t.Fatalf("B's view of A after the answer: %+v", got)
	}
	b.s.Hellos(ctx) // at once: knownKey, the new key
	if got := peer(t, a, bOnA.ID); got.OurKey != store.OurKeyCurrent {
		t.Fatalf("A's view of B after its next hello: %q", got.OurKey)
	}
	if _, _, ok := a.s.Keys.Old(); ok {
		t.Error("A still holds its old key, every peer moved")
	}
	// And on, under the new key.
	if n, err := b.s.SyncTitles(ctx, peer(t, b, aOnB.ID)); err != nil || n != 1 {
		t.Fatalf("B reading A's titles after: %d, %v", n, err)
	}
	noHits(t, b)
}

// Without A's commitment, B's operator confirms A's new key: B's hellos say
// "pending" meanwhile, A's panel shows it; once confirmed, B's next hello
// knows the new key. With another commitment, B refuses it, and says
// "refused".
func TestPulledRotationPendingRefused(t *testing.T) {
	ctx := context.Background()
	for _, refuse := range []bool{false, true} {
		a, b, bOnA, aOnB := uncallablePair(t)
		if refuse {
			exec(t, b, `UPDATE peers SET next_commitment = ?`, bytes.Repeat([]byte{1}, 32))
		} else {
			exec(t, b, `UPDATE peers SET next_commitment = NULL, commitment_from = ''`)
		}
		if err := a.s.Rotate(ctx, ""); err != nil {
			t.Fatal(err)
		}
		newKey := a.s.Keys.Current().Public()
		b.s.helloNow(aOnB.ID)
		b.s.Hellos(ctx)
		got := peer(t, b, aOnB.ID)
		switch {
		case msfp.SameKey(got.PublicKey, newKey):
			t.Fatalf("refused %v: B moved", refuse)
		case !refuse && (got.Pending == nil || !msfp.SameKey(got.Pending.New, newKey)):
			t.Fatalf("B's pending rotation: %+v", got.Pending)
		case refuse && !msfp.SameKey(got.RefusedKey, newKey):
			t.Fatalf("B's refused key: %x", got.RefusedKey)
		}
		want := msfp.StatePending
		if refuse {
			want = msfp.RotationRefused
		}
		b.s.helloNow(aOnB.ID)
		b.s.Hellos(ctx)
		if v := peer(t, a, bOnA.ID); v.OurKey != store.OurKeyOld || v.RotationAnswer != want {
			t.Fatalf("refused %v: A's view of B: %q %q", refuse, v.OurKey, v.RotationAnswer)
		}
		// Meanwhile A answers with its old key: B reads its titles.
		if n, err := b.s.SyncTitles(ctx, peer(t, b, aOnB.ID)); err != nil || n != 1 {
			t.Fatalf("refused %v: B reading A's titles: %d, %v", refuse, n, err)
		}
		if refuse {
			noHits(t, b)
			continue
		}
		if _, err := b.s.ConfirmRotation(ctx, aOnB.ID); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "A learning that B moved", func() bool { return peer(t, a, bOnA.ID).OurKey == store.OurKeyCurrent })
		noHits(t, b)
	}
}

// A's key was stolen: A revokes it and moves to its next key in one
// action (MSFP core section 12.7, case 1). B can't be called: one answer
// carries both; B suspends A, then lifts it with the committed key. With
// the commitment wrong, A stays suspended at B, which keeps sending its
// scheduled hellos and reads only their rotation.
func TestPulledRevocation(t *testing.T) {
	ctx := context.Background()
	for _, refuse := range []bool{false, true} {
		a, b, bOnA, aOnB := uncallablePair(t)
		if refuse {
			exec(t, b, `UPDATE peers SET next_commitment = ?`, bytes.Repeat([]byte{1}, 32))
		}
		revoked := a.s.Keys.Current().Public()
		if rotated, err := a.s.Revoke(ctx, "", false); err != nil || !rotated {
			t.Fatalf("revoking: %v, %v", rotated, err)
		}
		if !a.s.Keys.OldRevoked() {
			t.Fatal("A doesn't hold its old key as revoked")
		}
		newKey := a.s.Keys.Current().Public()
		b.s.helloNow(aOnB.ID)
		b.s.Hellos(ctx)
		got := peer(t, b, aOnB.ID)
		if !refuse {
			if got.Suspended != nil || !msfp.SameKey(got.PublicKey, newKey) {
				t.Fatalf("B's view of A: suspended %v, key moved %v", got.Suspended, msfp.SameKey(got.PublicKey, newKey))
			}
			b.s.Hellos(ctx)
			if v := peer(t, a, bOnA.ID); v.OurKey != store.OurKeyCurrent {
				t.Fatalf("A's view of B: %q", v.OurKey)
			}
			noHits(t, b)
			continue
		}
		if got.Suspended == nil || !msfp.SameKey(got.PublicKey, revoked) || !msfp.SameKey(got.RefusedKey, newKey) {
			t.Fatalf("B's view of A, the commitment wrong: %+v", got)
		}
		if _, err := b.s.SyncTitles(ctx, got); !errors.Is(err, ErrNotPaired) {
			t.Errorf("B reading a suspended A's titles: %v", err)
		}
		hits := a.routes.hits.Load()
		b.s.helloNow(aOnB.ID)
		b.s.Hellos(ctx)
		if a.routes.hits.Load() == hits {
			t.Error("no scheduled hello to the suspended A")
		}
		if _, err := b.s.Call(ctx, got, librariesPath, struct{}{}, &json.RawMessage{}); !errors.Is(err, ErrSuspended) {
			t.Errorf("another call to the suspended A: %v", err)
		}
		noHits(t, b)
	}
}

// callsFailed (MSFP core section 7.5): A's calls to B can't connect; B's
// hello is answered so; B's panel shows it, and nothing else changes. A
// call that connects again clears it.
func TestCallsFailedShown(t *testing.T) {
	ctx := context.Background()
	a, b, bOnA, aOnB := pairedPair(t)
	exec(t, a, `UPDATE peers SET address = 'https://127.0.0.1:1' WHERE id = ?`, bOnA.ID)
	if _, err := a.s.Check(ctx, peer(t, a, bOnA.ID)); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("A's check of B: %v", err)
	}
	if _, err := b.s.Check(ctx, aOnB); err != nil {
		t.Fatal(err)
	}
	got := peer(t, b, aOnB.ID)
	if !got.CallsFailed || !got.Callable() || b.s.Uncallable {
		t.Fatalf("B's view of A: callsFailed %v, callable %v", got.CallsFailed, got.Callable())
	}
	if v := peer(t, a, bOnA.ID); !v.Callable() {
		t.Fatalf("A's view of B: %q", v.Inbound)
	}
	exec(t, a, `UPDATE peers SET address = ? WHERE id = ?`, b.url, bOnA.ID)
	if _, err := a.s.Check(ctx, peer(t, a, bOnA.ID)); err != nil {
		t.Fatal(err)
	}
	b.s.Check(ctx, got)
	if peer(t, b, aOnB.ID).CallsFailed {
		t.Error("still shown once A's calls connect")
	}
}

// B, paired with A, turns "can't be called" on: A learns from its next
// hello, stores its address at once, drops its titles, and calls it no
// more. Turned off, B moves back to its address; A checks a hello there
// is answered under B's key before calling it again, then reads its
// titles.
func TestUncallableOnOff(t *testing.T) {
	ctx := context.Background()
	b, a, _, bOnA := pairedPair(t) // B offers its films to A
	if n, err := a.s.SyncTitles(ctx, bOnA); err != nil || n != 1 {
		t.Fatalf("A reading B's titles: %d, %v", n, err)
	}
	b = uncallableTo(t, b, true)
	if _, _, ok := b.s.Moving(); ok {
		t.Error("B keeps its former address, which no one calls")
	}
	b.routes.hits.Store(0)
	b.s.Hellos(ctx)
	got := peer(t, a, bOnA.ID)
	if got.Inbound != store.InboundNo || got.Address != b.s.Address {
		t.Fatalf("A's view of B: %q at %q", got.Inbound, got.Address)
	}
	if n, _ := a.s.Store.PeerTitleCounts(ctx); n[bOnA.ID] != 0 {
		t.Errorf("A kept B's titles: %v", n)
	}
	if _, err := a.s.SyncTitles(ctx, got); !errors.Is(err, ErrUncallable) {
		t.Errorf("A reading B's titles: %v", err)
	}
	noHits(t, b)

	b = uncallableTo(t, b, false)
	waitFor(t, "A confirming B's address", func() bool { return peer(t, a, bOnA.ID).Callable() })
	if got := peer(t, a, bOnA.ID); got.Address != b.site || got.Move != nil {
		t.Fatalf("A's view of B: %q, move %+v", got.Address, got.Move)
	}
	// Its titles read again at once, by themselves.
	waitFor(t, "A reading B's titles again", func() bool {
		n, _ := a.s.Store.PeerTitleCounts(ctx)
		return n[bOnA.ID] == 1
	})
}

// Until a hello at its address answers under its key, a peer that says it
// can be called again isn't (MSFP core section 7.5): at an address that
// doesn't answer, it stays as it was, but for being checked.
func TestUncallableConfirmFails(t *testing.T) {
	ctx := context.Background()
	a, b, bOnA, aOnB := uncallablePair(t)
	body, _ := json.Marshal(map[string]any{"state": "paired", "address": "https://127.0.0.1:1"})
	if status, _ := signedCall(t, b, aOnB, helloPath, body, b.s.Keys.Current()); status != http.StatusOK {
		t.Fatalf("B's hello: %d", status)
	}
	waitFor(t, "A trying B's address", func() bool {
		m := peer(t, a, bOnA.ID).Move
		return m != nil && !m.Failing.IsZero()
	})
	got := peer(t, a, bOnA.ID)
	if got.Callable() || got.Inbound != store.InboundConfirming || got.Address != b.s.Address {
		t.Fatalf("A's view of B: %q at %q", got.Inbound, got.Address)
	}
	if _, err := a.s.TheirLibraries(ctx, got); !errors.Is(err, ErrUncallable) {
		t.Errorf("A reading B's libraries meanwhile: %v", err)
	}
	noHits(t, b)
}

// B's hello, as a server that can't be called sends it, is the one of
// vectors/v1/exchanges.json: inbound false, its address of that form,
// knownKey, no pins.
func TestUncallableHelloVector(t *testing.T) {
	raw, err := os.ReadFile("../msfp/testdata/v1/exchanges.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Exchanges []struct{ Name, Body string }
	}
	json.Unmarshal(raw, &v)
	i := slices.IndexFunc(v.Exchanges, func(x struct{ Name, Body string }) bool {
		return x.Name == "hello, from a server that can't be called"
	})
	if i < 0 {
		t.Fatal("no such exchange")
	}
	var want map[string]any
	json.Unmarshal([]byte(v.Exchanges[i].Body), &want)
	a, _ := msfp.Decode64(want["knownKey"].(string), 32)
	s := &Service{Uncallable: true, Address: want["address"].(string)}
	body, err := msfp.Marshal(s.ourHello(context.Background(), &store.Peer{PublicKey: a}, helloPaired, false))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	json.Unmarshal(body, &got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("B's hello %s\nthe exchange's %s", body, v.Exchanges[i].Body)
	}
}
