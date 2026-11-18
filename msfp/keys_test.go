package msfp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/overfin/server/msfp"
	"github.com/overfin/server/msfp/store/jsonfile"
	"github.com/overfin/server/msfp/wire"
)

// Changes of key (MSFP core section 12), between engines on one fake
// clock, and by requests signed by hand with a server's keys.

// The test keys of the vectors (keys.json): a, then a2, a3 as A's next
// keys; b2 another.
const (
	seedA  = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	seedA2 = "808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f"
	seedA3 = "a0a1a2a3a4a5a6a7a8a9aaabacadaeafb0b1b2b3b4b5b6b7b8b9babbbcbdbebf"
	seedB2 = "c0c1c2c3c4c5c6c7c8c9cacbcccdcecfd0d1d2d3d4d5d6d7d8d9dadbdcdddedf"
)

func keyOf(t *testing.T, seed string) wire.Key {
	t.Helper()
	b, _ := hex.DecodeString(seed)
	k, err := wire.KeyFromSeed(b)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// keyServer is an engine on clock whose first draws of 32 bytes are
// seeds: its key, then its next key.
func keyServer(t *testing.T, clock *fakeClock, seeds ...string) *server {
	t.Helper()
	r := &script{}
	r.queue(seeds...)
	return newServer(t, msfp.Config{Clock: clock, Random: r})
}

// link pairs redeemer with maker (a code of maker's, accepted): the
// peer's ID on each.
func link(t *testing.T, maker, redeemer *server) (onMaker, onRedeemer string) {
	t.Helper()
	ctx := context.Background()
	code, _, err := maker.eng.MakeCode(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := redeemer.eng.RedeemCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	id := peerBy(t, maker, fingerprint(t, redeemer)).ID
	if _, err := maker.eng.Accept(ctx, id); err != nil {
		t.Fatal(err)
	}
	return id, v.ID
}

func fingerprint(t *testing.T, s *server) string {
	t.Helper()
	v, err := s.eng.Server(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return v.Fingerprint
}

func serverView(t *testing.T, s *server) msfp.ServerView {
	t.Helper()
	v, err := s.eng.Server(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// peerBy is s's peer of that fingerprint.
func peerBy(t *testing.T, s *server, fp string) msfp.PeerView {
	t.Helper()
	peers, err := s.eng.Peers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range peers {
		if p.Fingerprint == fp {
			return p
		}
	}
	t.Fatalf("no peer %s in %+v", fp, peers)
	return msfp.PeerView{}
}

func peerView(t *testing.T, s *server, id string) msfp.PeerView {
	t.Helper()
	v, err := s.eng.Peer(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// rawCall sends s a call signed with k at now, as a peer would.
func rawCall(t *testing.T, s *server, k wire.Key, call string, body any, now time.Time) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, s.srv.URL+"/api/v1/federation/"+call, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	wire.SignRequest(r, b, k, now)
	w := httptest.NewRecorder()
	s.eng.FederationHandler().ServeHTTP(w, r)
	return w
}

var paired = map[string]string{"state": "paired"}

// signed says whether an answer is signed at all.
func signed(w *httptest.ResponseRecorder) bool { return w.Header().Get("MSFP-Signature") != "" }

// waitFor waits a few seconds at most for what the background does.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 500 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("never:", what)
}

func countEvents[T msfp.Event](s *server) int {
	n := 0
	for _, ev := range s.events.all() {
		if _, ok := ev.(T); ok {
			n++
		}
	}
	return n
}

func editPeer(t *testing.T, s *server, id string, change func(*msfp.PeerRecord)) {
	t.Helper()
	err := s.store.Update(context.Background(), func(tx msfp.Tx) error {
		p, err := tx.Peer(id)
		if err != nil || p == nil {
			return errors.Join(err, errors.New("no peer"))
		}
		change(p)
		return tx.PutPeer(*p)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A rotates to the key it committed to: B and C move at once, the change
// ends, and B still hears a for 10 minutes: a retry of the same rotation
// is answered rotated again and changes nothing, a rotation to another
// key from it is refused.
func TestRotation(t *testing.T) {
	ctx := context.Background()
	clock := newClock(1800000000)
	a := keyServer(t, clock, seedA, seedA2)
	b := newServer(t, msfp.Config{Clock: clock})
	c := newServer(t, msfp.Config{Clock: clock})
	bOnA, aOnB := link(t, a, b)
	link(t, a, c)
	before := serverView(t, a)
	if before.NextKey != wire.Fingerprint(keyOf(t, seedA2).Public()) || !before.NextOnServer {
		t.Fatalf("A's next key: %+v", before)
	}

	if err := a.eng.Rotate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	after := serverView(t, a)
	if after.Fingerprint != before.NextKey || after.NextKey == before.NextKey || !after.NextOnServer ||
		!after.RotationSince.IsZero() || after.NotMoved != 0 {
		t.Errorf("A after: %+v", after)
	}
	if n := countEvents[msfp.RotationAnswered](a); n != 2 || countEvents[msfp.RotationEnded](a) != 1 {
		t.Errorf("A's events: %#v", a.events.all())
	}
	onB := peerView(t, b, aOnB)
	if onB.Fingerprint != after.Fingerprint || onB.CommitmentFrom != "rotation" || countEvents[msfp.PeerRotated](b) != 1 {
		t.Errorf("A on B: %+v", onB)
	}
	if p := peerView(t, a, bOnA); p.OurKey != msfp.KeyCurrent {
		t.Errorf("B on A: %+v", p)
	}
	if _, err := a.eng.Check(ctx, bOnA); err != nil {
		t.Fatal("A's hello under its new key:", err)
	}

	// The same rotation again, signed with a: rotated, nothing changes.
	ka, ka2 := keyOf(t, seedA), keyOf(t, seedA2)
	clock.Add(time.Minute)
	rec := peerRecord(t, b.store, aOnB)
	again := wire.SignRotation(ka, ka2, rec.Commitment, clock.Now())
	if w := rawCall(t, b, ka, "rotate", again.Body(), clock.Now()); w.Code != 200 || w.Body.String() != `{"state":"rotated"}` {
		t.Errorf("rotate again: %d %s", w.Code, w.Body)
	}
	if rec := peerRecord(t, b.store, aOnB); len(rec.Rotations) != 1 || !bytes.Equal(rec.Key, ka2.Public()) {
		t.Errorf("A on B after the retry: %+v", rec)
	}
	// Another key, signed with a: refused.
	other := wire.SignRotation(ka, wire.NewKey(), nil, clock.Now())
	if w := rawCall(t, b, ka, "rotate", other.Body(), clock.Now()); w.Code != 409 || !signed(w) {
		t.Errorf("rotate from the previous key to another: %d %s", w.Code, w.Body)
	}
	// a heard for 10 minutes, then not at all.
	clock.Add(4 * time.Minute)
	if w := rawCall(t, b, ka, "hello", paired, clock.Now()); w.Code != 200 {
		t.Errorf("hello under a, 5 minutes after: %d", w.Code)
	}
	clock.Add(6 * time.Minute)
	if w := rawCall(t, b, ka, "hello", paired, clock.Now()); w.Code != 404 || signed(w) {
		t.Errorf("hello under a, 11 minutes after: %d, signed %v", w.Code, signed(w))
	}
}

// A rotation that doesn't match the commitment is refused and shown; one
// with no commitment held waits for the operator, through repeats, and
// once confirmed the rotating server learns it at its next contact.
func TestRotationRefusedAndPending(t *testing.T) {
	ctx := context.Background()
	clock := newClock(1800000000)
	a := keyServer(t, clock, seedA, seedA2)
	b := newServer(t, msfp.Config{Clock: clock})
	bOnA, aOnB := link(t, a, b)
	ka, ka2 := keyOf(t, seedA), keyOf(t, seedA2)

	// To a key it didn't commit to: 409, a warning, the key kept.
	thief := wire.NewKey()
	w := rawCall(t, b, ka, "rotate", wire.SignRotation(ka, thief, nil, clock.Now()).Body(), clock.Now())
	if w.Code != 409 || !bytes.Contains(w.Body.Bytes(), []byte(`"rotation-refused"`)) || !signed(w) {
		t.Errorf("mismatch: %d %s", w.Code, w.Body)
	}
	v := peerView(t, b, aOnB)
	if v.Refused == nil || v.Refused.Fingerprint != wire.Fingerprint(thief.Public()) || v.Fingerprint != wire.Fingerprint(ka.Public()) ||
		countEvents[msfp.RotationRefused](b) != 1 {
		t.Errorf("A on B: %+v", v)
	}
	if err := b.eng.DismissRefused(ctx, aOnB); err != nil || peerView(t, b, aOnB).Refused != nil {
		t.Errorf("dismiss: %v", err)
	}

	// No commitment held (as after a rotation statement with "-"): A's
	// rotate is answered pending.
	editPeer(t, b, aOnB, func(p *msfp.PeerRecord) { p.Commitment, p.CommitmentFrom = nil, "" })
	if err := a.eng.Rotate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if p := peerView(t, a, bOnA); p.OurKey != msfp.KeyOld || p.RotationAnswer != "pending" {
		t.Errorf("B on A: %+v", p)
	}
	if sv := serverView(t, a); sv.RotationSince.IsZero() || sv.NotMoved != 1 {
		t.Errorf("A: %+v", sv)
	}
	v = peerView(t, b, aOnB)
	if v.Pending == nil || v.Pending.Fingerprint != wire.Fingerprint(ka2.Public()) || countEvents[msfp.RotationPending](b) != 1 {
		t.Errorf("A on B: %+v", v)
	}
	// Repeated: still pending, no new warning.
	clock.Add(time.Second)
	repeat := wire.SignRotation(ka, ka2, wire.Commitment(wire.NewKey().Public()), clock.Now())
	if w := rawCall(t, b, ka, "rotate", repeat.Body(), clock.Now()); w.Code != 202 || w.Body.String() != `{"state":"pending"}` {
		t.Errorf("repeat: %d %s", w.Code, w.Body)
	}
	if countEvents[msfp.RotationPending](b) != 1 {
		t.Error("a repeat warned again")
	}
	// The operator confirms, after the 10 seconds between two rotates to
	// a peer whose answer was pending.
	clock.Add(11 * time.Second)
	v, err := b.eng.ConfirmRotation(ctx, aOnB)
	if err != nil || v.Fingerprint != wire.Fingerprint(ka2.Public()) || v.Pending != nil {
		t.Fatalf("confirm: %+v %v", v, err)
	}
	// B's hello reached A, which sent its rotate again: B answered rotated.
	waitFor(t, "A sees B moved", func() bool { return peerView(t, a, bOnA).OurKey == msfp.KeyCurrent })
	if sv := serverView(t, a); !sv.RotationSince.IsZero() {
		t.Errorf("A's change not over: %+v", sv)
	}
	if _, err := b.eng.ConfirmRotation(ctx, aOnB); !errors.Is(err, msfp.ErrNoPending) {
		t.Errorf("confirm again: %v", err)
	}
	if _, err := b.eng.Check(ctx, aOnB); err != nil {
		t.Errorf("B's hello now: %v", err)
	}
}

// A retired key never becomes current again, nor another peer's; a
// pending key is forgotten when the pairing ends.
func TestRotationRetiredAndEnded(t *testing.T) {
	ctx := context.Background()
	clock := newClock(1800000000)
	a := keyServer(t, clock, seedA, seedA2)
	b := newServer(t, msfp.Config{Clock: clock})
	c := keyServer(t, clock, seedB2)
	_, aOnB := link(t, a, b)
	link(t, b, c)
	ka, ka2 := keyOf(t, seedA), keyOf(t, seedA2)

	// a to a2, committing to none.
	if w := rawCall(t, b, ka, "rotate", wire.SignRotation(ka, ka2, nil, clock.Now()).Body(), clock.Now()); w.Code != 200 {
		t.Fatalf("a to a2: %d %s", w.Code, w.Body)
	}
	if v := peerView(t, b, aOnB); v.CommitmentFrom != "" {
		t.Errorf("A on B: %+v", v)
	}
	// Back to a: refused, not pending.
	clock.Add(time.Second)
	if w := rawCall(t, b, ka2, "rotate", wire.SignRotation(ka2, ka, nil, clock.Now()).Body(), clock.Now()); w.Code != 409 {
		t.Errorf("back to a: %d %s", w.Code, w.Body)
	}
	// To C's key: refused.
	if w := rawCall(t, b, ka2, "rotate", wire.SignRotation(ka2, keyOf(t, seedB2), nil, clock.Now()).Body(), clock.Now()); w.Code != 409 {
		t.Errorf("to another peer's key: %d %s", w.Code, w.Body)
	}
	if v := peerView(t, b, aOnB); v.Pending != nil {
		t.Errorf("pending: %+v", v)
	}
	// To a new key: pending. Then B ends the pairing: the pending key is
	// forgotten too, its requests get a signed 404.
	n := wire.NewKey()
	if w := rawCall(t, b, ka2, "rotate", wire.SignRotation(ka2, n, nil, clock.Now()).Body(), clock.Now()); w.Code != 202 {
		t.Fatalf("to n: %d %s", w.Code, w.Body)
	}
	if err := b.eng.End(ctx, aOnB); err != nil {
		t.Fatal(err)
	}
	for name, k := range map[string]wire.Key{"pending": n, "current": ka2, "former": ka} {
		if w := rawCall(t, b, k, "hello", paired, clock.Now()); w.Code != 404 || !signed(w) {
			t.Errorf("%s key after the end: %d, signed %v", name, w.Code, signed(w))
		}
	}
	var forgotten *msfp.ForgottenKey
	sum := sha256.Sum256(n.Public())
	b.store.Read(ctx, func(tx msfp.Tx) error {
		var err error
		forgotten, err = tx.Forgotten(sum[:])
		return err
	})
	if forgotten == nil {
		t.Error("the pending key isn't forgotten")
	}
}

// KeyStolen with the next key: B suspends a, then moves to a2 at once,
// a never heard again; C, down then, moves at its next contact.
func TestKeyStolenWithNext(t *testing.T) {
	ctx := context.Background()
	clock := newClock(1800000000)
	a := keyServer(t, clock, seedA, seedA2)
	b := newServer(t, msfp.Config{Clock: clock})
	c := newServer(t, msfp.Config{Clock: clock})
	_, aOnB := link(t, a, b)
	cOnA, aOnC := link(t, a, c)
	c.setDown(true)

	rotated, err := a.eng.KeyStolen(ctx, "", false)
	if err != nil || !rotated {
		t.Fatalf("stolen: %v %v", rotated, err)
	}
	v := peerView(t, b, aOnB)
	if v.Fingerprint != wire.Fingerprint(keyOf(t, seedA2).Public()) || !v.Suspended.IsZero() ||
		countEvents[msfp.PeerSuspended](b) != 1 || countEvents[msfp.PeerResumed](b) != 1 {
		t.Errorf("A on B: %+v %#v", v, b.events.all())
	}
	if rec := peerRecord(t, b.store, aOnB); !rec.PreviousUntil.IsZero() {
		t.Errorf("a still heard until %v", rec.PreviousUntil)
	}
	if w := rawCall(t, b, keyOf(t, seedA), "hello", paired, clock.Now()); w.Code != 404 || signed(w) {
		t.Errorf("a, revoked, heard: %d", w.Code)
	}
	if sv := serverView(t, a); sv.RotationSince.IsZero() || sv.NotMoved != 1 {
		t.Errorf("A: %+v", sv)
	}
	if p := peerView(t, a, cOnA); p.OurKey != msfp.KeyOld {
		t.Errorf("C on A: %+v", p)
	}

	// C comes back and calls, after the minute between two rotates: A's
	// rotate follows, at that contact.
	c.setDown(false)
	clock.Add(2 * time.Minute)
	if _, err := c.eng.Check(ctx, aOnC); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "C moves", func() bool { return peerView(t, a, cOnA).OurKey == msfp.KeyCurrent })
	if sv := serverView(t, a); !sv.RotationSince.IsZero() {
		t.Errorf("A's change not over: %+v", sv)
	}
	if v := peerView(t, c, aOnC); v.Fingerprint != serverView(t, a).Fingerprint {
		t.Errorf("A on C: %+v", v)
	}
	// Revoked again by hand, a is still not heard; ErrNoNextKey only when
	// asked to rotate with no seed and no text.
	if _, _, err := a.eng.ExportNextKey(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.eng.DeleteNextKey(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.eng.KeyStolen(ctx, "", false); !errors.Is(err, msfp.ErrNoNextKey) {
		t.Errorf("no next key: %v", err)
	}
	if _, err := a.eng.KeyStolen(ctx, "msfp-next-key-1."+wire.Encode64(make([]byte, 32)), false); !errors.Is(err, msfp.ErrNotNextKey) {
		t.Errorf("not the next key: %v", err)
	}
}

// The next key exported, deleted, then given back to Rotate, white space
// and all.
func TestRotateWithExportedKey(t *testing.T) {
	ctx := context.Background()
	clock := newClock(1800000000)
	a := keyServer(t, clock, seedA, seedA2)
	b := newServer(t, msfp.Config{Clock: clock})
	link(t, a, b)
	text, fp, err := a.eng.ExportNextKey(ctx)
	if err != nil || text != "msfp-next-key-1."+wire.Encode64(keyOf(t, seedA2).Seed()) || fp != serverView(t, a).NextKey {
		t.Fatalf("export: %q %q %v", text, fp, err)
	}
	if err := a.eng.DeleteNextKey(ctx); err != nil {
		t.Fatal(err)
	}
	if sv := serverView(t, a); sv.NextOnServer || sv.NextKey != fp {
		t.Errorf("after delete: %+v", sv)
	}
	if err := a.eng.Rotate(ctx, ""); !errors.Is(err, msfp.ErrNoNextKey) {
		t.Errorf("no seed: %v", err)
	}
	if err := a.eng.Rotate(ctx, "overfin-next-key-1."+text[len("msfp-next-key-1."):]); !errors.Is(err, msfp.ErrNotNextKey) {
		t.Errorf("another prefix: %v", err)
	}
	spaced := text[:20] + " \n\t" + text[20:] + "\n"
	if err := a.eng.Rotate(ctx, spaced); err != nil {
		t.Fatal(err)
	}
	if sv := serverView(t, a); sv.Fingerprint != fp || !sv.NextOnServer {
		t.Errorf("after: %+v", sv)
	}
}

// KeyStolen without the next key (MSFP core section 12.7, case 2): a new
// key, every pairing ended; B, reached by revoke, suspends A; C, down
// then, learns it from the revocation-only answer to its hello; D, whose
// pairing A's operator ended before, gets the 404 of a forgotten key,
// signed with A's new key, which D can't check. After 30 days, B's A is
// gone, still suspended, and E, back only then, gets that 404 too.
func TestKeyStolenWithoutNext(t *testing.T) {
	ctx := context.Background()
	clock := newClock(1800000000)
	a := keyServer(t, clock, seedA, seedA2)
	b := newServer(t, msfp.Config{Clock: clock})
	c := newServer(t, msfp.Config{Clock: clock})
	d := newServer(t, msfp.Config{Clock: clock})
	e := newServer(t, msfp.Config{Clock: clock})
	_, aOnB := link(t, a, b)
	_, aOnC := link(t, a, c)
	dOnA, aOnD := link(t, a, d)
	_, aOnE := link(t, a, e)
	ka := keyOf(t, seedA)

	d.setDown(true)
	if err := a.eng.End(ctx, dOnA); err != nil {
		t.Fatal(err)
	}
	c.setDown(true)
	e.setDown(true)
	rotated, err := a.eng.KeyStolen(ctx, "", true)
	if err != nil || rotated {
		t.Fatalf("stolen: %v %v", rotated, err)
	}
	sv := serverView(t, a)
	if sv.Fingerprint == wire.Fingerprint(ka.Public()) || sv.NextKey == wire.Fingerprint(keyOf(t, seedA2).Public()) ||
		sv.RevokedAt.IsZero() || !sv.NextOnServer {
		t.Errorf("A: %+v", sv)
	}
	if peers, _ := a.eng.Peers(ctx); len(peers) != 0 || countEvents[msfp.PeerEnded](a) != 4 {
		t.Errorf("A's peers: %+v", peers)
	}

	// B: suspended. No call goes to it; only rotate and revoke are heard
	// from a, and a rotation to a key it didn't commit to is refused.
	if v := peerView(t, b, aOnB); v.Suspended.IsZero() || countEvents[msfp.PeerSuspended](b) != 1 {
		t.Errorf("A on B: %+v", v)
	}
	if _, err := b.eng.Check(ctx, aOnB); !errors.Is(err, msfp.ErrSuspended) {
		t.Errorf("B's hello: %v", err)
	}
	if w := rawCall(t, b, ka, "hello", paired, clock.Now()); w.Code != 404 || !signed(w) {
		t.Errorf("hello from a: %d", w.Code)
	}
	if w := rawCall(t, b, ka, "revoke", wire.SignRevocation(ka, clock.Now()).Body(), clock.Now()); w.Code != 200 ||
		w.Body.String() != `{"state":"suspended"}` {
		t.Errorf("revoke again: %d %s", w.Code, w.Body)
	}
	if w := rawCall(t, b, ka, "rotate", wire.SignRotation(ka, wire.NewKey(), nil, clock.Now()).Body(), clock.Now()); w.Code != 409 {
		t.Errorf("rotate of a suspended key elsewhere: %d", w.Code)
	}
	if v := peerView(t, b, aOnB); v.Suspended.IsZero() || v.Refused == nil {
		t.Errorf("A on B: %+v", v)
	}

	// C: its hello is answered with the revocation alone, signed with a.
	c.setDown(false)
	clock.Add(time.Hour)
	if _, err := c.eng.Check(ctx, aOnC); err != nil {
		t.Fatal(err)
	}
	if v := peerView(t, c, aOnC); v.Suspended.IsZero() || countEvents[msfp.PeerSuspended](c) != 1 {
		t.Errorf("A on C: %+v", v)
	}
	if x := a.exchanges("hello"); len(x) == 0 || !bytes.HasPrefix([]byte(x[len(x)-1].resBody), []byte(`{"revocation":{"date":`)) {
		t.Errorf("A's answer to C: %+v", x)
	}
	// D: ended by A's operator before, the 404 of a forgotten key, signed
	// with the new key: unsigned to D.
	d.setDown(false)
	if _, err := d.eng.Check(ctx, aOnD); !errors.Is(err, msfp.ErrUnreachable) || !peerView(t, d, aOnD).Suspended.IsZero() {
		t.Errorf("D's hello: %v", err)
	}
	if x := a.exchanges("hello"); x[len(x)-1].status != 404 || x[len(x)-1].resSig == "" {
		t.Errorf("A's answer to D: %+v", x[len(x)-1])
	}

	// 30 days.
	clock.Add(30 * 24 * time.Hour)
	if err := a.eng.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if sv := serverView(t, a); !sv.RevokedAt.IsZero() {
		t.Errorf("A still answers with the revocation: %+v", sv)
	}
	if err := b.eng.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if v := peerView(t, b, aOnB); v.State != msfp.Gone || v.Suspended.IsZero() || countEvents[msfp.PeerGone](b) != 1 {
		t.Errorf("A on B after 30 days: %+v", v)
	}
	if w := rawCall(t, b, ka, "hello", paired, clock.Now()); w.Code != 404 || peerView(t, b, aOnB).State != msfp.Gone {
		t.Errorf("a paired hello brought it back: %d", w.Code)
	}
	e.setDown(false)
	if _, err := e.eng.Check(ctx, aOnE); !errors.Is(err, msfp.ErrUnreachable) || !peerView(t, e, aOnE).Suspended.IsZero() {
		t.Errorf("E's hello after 30 days: %v", err)
	}
	// E, after the 30 days: the plain 404 of an unknown key, not one
	// signed with a key it can't know (section 12.7, case 2).
	if x := a.exchanges("hello"); x[len(x)-1].status != 404 || x[len(x)-1].resSig != "" {
		t.Errorf("A's answer to E: %+v", x[len(x)-1])
	}
}

// Two changes in a row: the first ends as the second starts, its
// stragglers lost; the first's late ends never touch the second.
func TestTwoChangesInARow(t *testing.T) {
	ctx := context.Background()
	clock := newClock(1800000000)
	a := keyServer(t, clock, seedA, seedA2)
	b := newServer(t, msfp.Config{Clock: clock})
	c := newServer(t, msfp.Config{Clock: clock})
	bOnA, aOnB := link(t, a, b)
	cOnA, _ := link(t, a, c)
	ka, ka2 := keyOf(t, seedA), keyOf(t, seedA2)

	c.setDown(true)
	if err := a.eng.Rotate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if sv := serverView(t, a); sv.NotMoved != 1 {
		t.Fatalf("A: %+v", sv)
	}
	// The second: C, still on a, is lost; B, down now, stays on a2.
	b.setDown(true)
	clock.Add(time.Hour)
	if err := a.eng.Rotate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	second := serverView(t, a)
	if second.NotMoved != 1 || !second.RotationSince.Equal(clock.Now()) {
		t.Errorf("A: %+v", second)
	}
	if p := peerView(t, a, cOnA); p.OurKey != msfp.KeyLost || countEvents[msfp.OurKeyLost](a) != 1 {
		t.Errorf("C on A: %+v", p)
	}
	if p := peerView(t, a, bOnA); p.OurKey != msfp.KeyOld {
		t.Errorf("B on A: %+v", p)
	}

	// Late: the first change's end, and B's move to a2, learnt now.
	if ended, err := a.eng.FinishRotationForTest(ctx, ka.Public()); ended || err != nil {
		t.Errorf("the first change ended again: %v %v", ended, err)
	}
	if a.eng.PeerMovedForTest(ctx, bOnA, ka2.Public()) {
		t.Error("B moved to the key that is old now")
	}
	if sv := serverView(t, a); sv.RotationSince != second.RotationSince || sv.NotMoved != 1 || sv.Fingerprint != second.Fingerprint {
		t.Errorf("the second change touched: %+v", sv)
	}
	if p := peerView(t, a, bOnA); p.OurKey != msfp.KeyOld {
		t.Errorf("B on A: %+v", p)
	}

	// B back: the daily rotate moves it, and the second change ends.
	b.setDown(false)
	clock.Add(25 * time.Hour)
	if err := a.eng.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if sv := serverView(t, a); !sv.RotationSince.IsZero() {
		t.Errorf("A: %+v", sv)
	}
	if v := peerView(t, b, aOnB); v.Fingerprint != second.Fingerprint {
		t.Errorf("A on B: %+v", v)
	}
	// C: lost; A calls it no more.
	c.setDown(false)
	if _, err := a.eng.Check(ctx, cOnA); !errors.Is(err, msfp.ErrKeyLost) {
		t.Errorf("A's hello to C: %v", err)
	}
}

// A peer that never moves is lost after 30 days, and the old key goes.
func TestRotationThirtyDays(t *testing.T) {
	ctx := context.Background()
	clock := newClock(1800000000)
	a := keyServer(t, clock, seedA, seedA2)
	b := newServer(t, msfp.Config{Clock: clock})
	bOnA, _ := link(t, a, b)
	b.setDown(true)
	if err := a.eng.Rotate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	for day := 1; day <= 30; day++ {
		clock.Add(24 * time.Hour)
		if err := a.eng.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if sv := serverView(t, a); !sv.RotationSince.IsZero() || countEvents[msfp.OurKeyLost](a) != 1 {
		t.Errorf("A: %+v", sv)
	}
	if p := peerView(t, a, bOnA); p.OurKey != msfp.KeyLost {
		t.Errorf("B on A: %+v", p)
	}
	// It was told daily: one rotate tried a day (the server was down).
	if n := len(b.exchanges("rotate")); n != 0 {
		t.Errorf("rotates served while down: %d", n)
	}
}

// On the JSON file store, a restart in the middle of a change: the old
// key and who moved are kept; the peer that hadn't moves at the daily
// rotate after the restart.
func TestRotationJSONRestart(t *testing.T) {
	ctx := context.Background()
	clock := newClock(1800000000)
	dir := t.TempDir()
	st, err := jsonfile.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	random := &script{}
	random.queue(seedA, seedA2)
	a := newServer(t, msfp.Config{Clock: clock, Random: random, Store: st})
	b := newServer(t, msfp.Config{Clock: clock})
	c := newServer(t, msfp.Config{Clock: clock})
	bOnA, _ := link(t, a, b)
	cOnA, aOnC := link(t, a, c)
	c.setDown(true)
	if err := a.eng.Rotate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	before := serverView(t, a)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = jsonfile.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	clock.Add(time.Hour)
	a.reopen(t, msfp.Config{Clock: clock, Store: st})
	sv := serverView(t, a)
	if sv.Fingerprint != before.Fingerprint || !sv.RotationSince.Equal(before.RotationSince) || sv.NotMoved != 1 || sv.NextKey != before.NextKey {
		t.Fatalf("after the restart: %+v, was %+v", sv, before)
	}
	if p := peerView(t, a, bOnA); p.OurKey != msfp.KeyCurrent {
		t.Errorf("B on A: %+v", p)
	}
	if _, err := a.eng.Check(ctx, bOnA); err != nil {
		t.Errorf("A's hello to B, under the new key: %v", err)
	}
	c.setDown(false)
	clock.Add(24 * time.Hour)
	if err := a.eng.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if p := peerView(t, a, cOnA); p.OurKey != msfp.KeyCurrent {
		t.Errorf("C on A: %+v", p)
	}
	if v := peerView(t, c, aOnC); v.Fingerprint != sv.Fingerprint {
		t.Errorf("A on C: %+v", v)
	}
	if sv := serverView(t, a); !sv.RotationSince.IsZero() {
		t.Errorf("A: %+v", sv)
	}
}
