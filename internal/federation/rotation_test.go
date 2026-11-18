package federation

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/msfp"
	"github.com/overfin/server/internal/store"
)

// exec runs a statement on s's database: what no call can set, such as a
// pairing made before commitments.
func exec(t *testing.T, s *server, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+s.db+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

// sharedClock puts servers on one test clock, still until moved: their
// requests are dated by it and checked against it.
func sharedClock(servers ...*server) *clock {
	c := &clock{t: time.Now()}
	for _, s := range servers {
		s.s.Now = c.now
	}
	return c
}

func peer(t *testing.T, s *server, id string) *store.Peer {
	t.Helper()
	p, err := s.s.Store.PeerByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// signedCall sends p, from s, a request of body signed with key, and
// gives its answer's status and body.
func signedCall(t *testing.T, s *server, p *store.Peer, path string, body []byte, key msfp.Key) (int, string) {
	t.Helper()
	req, err := http.NewRequest("POST", p.Address+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	msfp.SignRequest(req, body, key, s.s.now())
	res, err := s.s.conns(p, true).client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	answer, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(answer)
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := msfp.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Pairing exchanges commitments both ways (MSFP core section 9.1); A
// changes its key to the committed one, and B follows at once, with no
// operator: the pairing goes on under the new key, the IDs B knows stay,
// and A drops its old key once B moved.
func TestRotateCommitmentMatch(t *testing.T) {
	ctx := context.Background()
	a, b, bOnA, aOnB := pairedPair(t)
	if !bytes.Equal(aOnB.NextCommitment, a.s.Keys.NextCommitment()) || aOnB.CommitmentFrom != store.CommitmentPair ||
		!bytes.Equal(bOnA.NextCommitment, b.s.Keys.NextCommitment()) || bOnA.CommitmentFrom != store.CommitmentPair {
		t.Fatalf("commitments from pair: B holds %x (%s), A holds %x (%s)", aOnB.NextCommitment, aOnB.CommitmentFrom,
			bOnA.NextCommitment, bOnA.CommitmentFrom)
	}
	old := a.s.Keys.Current()
	committed := a.s.Keys.NextCommitment()
	id := a.s.Keys.Opaque("version", bOnA.ID, "x")
	if err := a.s.Rotate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	now := a.s.Keys.Current()
	if msfp.SameKey(now.Public(), old.Public()) || !bytes.Equal(msfp.Commitment(now.Public()), committed) {
		t.Fatal("A isn't on its committed next key")
	}
	if bytes.Equal(a.s.Keys.NextCommitment(), committed) {
		t.Fatal("A has no new next key")
	}
	got := peer(t, b, aOnB.ID)
	if !msfp.SameKey(got.PublicKey, now.Public()) || !msfp.SameKey(got.PreviousKey, old.Public()) ||
		got.CommitmentFrom != store.CommitmentRotation || !bytes.Equal(got.NextCommitment, a.s.Keys.NextCommitment()) {
		t.Fatalf("B's view of A after the rotation: %+v", got)
	}
	if h, _ := b.s.Store.PeerRotations(ctx, aOnB.ID); len(h) != 1 || !msfp.SameKey(h[0].New, now.Public()) {
		t.Fatalf("B's history of A: %+v", h)
	}
	if got := peer(t, a, bOnA.ID); got.OurKey != store.OurKeyCurrent {
		t.Fatalf("A's view of B: %q", got.OurKey)
	}
	if _, _, ok := a.s.Keys.Old(); ok {
		t.Fatal("A kept its old key, every peer moved")
	}
	if a.s.Keys.Opaque("version", bOnA.ID, "x") != id {
		t.Fatal("an opaque ID changed with the key")
	}
	// The pairing goes on: B reads A's titles, both ask each other.
	if n, err := b.s.SyncTitles(ctx, peer(t, b, aOnB.ID)); err != nil || n != 1 {
		t.Fatalf("B reading A's titles: %d, %v", n, err)
	}
	if _, err := a.s.Check(ctx, peer(t, a, bOnA.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.s.Check(ctx, peer(t, b, aOnB.ID)); err != nil {
		t.Fatal(err)
	}
}

// A rotation to a key that doesn't match the commitment is refused, 409
// rotation-refused: B keeps the key it knows, and shows its operator the
// attempt (MSFP core section 12.3, case 3).
func TestRotateCommitmentMismatch(t *testing.T) {
	ctx := context.Background()
	a, b, _, aOnB := pairedPair(t)
	aOnBFromA := onlyPeer(t, a)
	thief := msfp.NewKey()
	rot := msfp.SignRotation(a.s.Keys.Current(), thief, nil, a.s.now())
	status, body := signedCall(t, a, aOnBFromA, rotatePath, marshal(t, rot.Body()), a.s.Keys.Current())
	if status != http.StatusConflict || !strings.Contains(body, "rotation-refused") {
		t.Fatalf("a rotation to another key: %d %s", status, body)
	}
	got := peer(t, b, aOnB.ID)
	if !msfp.SameKey(got.PublicKey, a.s.Keys.Current().Public()) || !msfp.SameKey(got.RefusedKey, thief.Public()) || got.RefusedAt == nil {
		t.Fatalf("B's view of A: %+v", got)
	}
	if err := b.s.DismissRefused(ctx, aOnB.ID); err != nil || peer(t, b, aOnB.ID).RefusedKey != nil {
		t.Fatalf("dismissing: %v", err)
	}
	// A bad statement is a 400.
	bad := rot.Body()
	bad.Date++
	if status, _ := signedCall(t, a, aOnBFromA, rotatePath, marshal(t, bad), a.s.Keys.Current()); status != http.StatusBadRequest {
		t.Fatalf("a bad statement: %d", status)
	}
}

// Without a commitment (a pairing made before them), a rotation waits for
// B's operator to confirm the new key (202 pending, MSFP core section
// 12.3, case 4); repeated, it stays one pending rotation. Once confirmed,
// A learns at the next contact, and the pairing goes on.
func TestRotateWithoutCommitment(t *testing.T) {
	ctx := context.Background()
	a, b, bOnA, aOnB := pairedPair(t)
	c := sharedClock(a, b)
	exec(t, b, `UPDATE peers SET next_commitment = NULL, commitment_from = ''`)
	if err := a.s.Rotate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	got := peer(t, b, aOnB.ID)
	newKey := a.s.Keys.Current().Public()
	if got.Pending == nil || !msfp.SameKey(got.Pending.New, newKey) || msfp.SameKey(got.PublicKey, newKey) {
		t.Fatalf("B's view of A: %+v", got)
	}
	if v := peer(t, a, bOnA.ID); v.OurKey != store.OurKeyOld || v.RotationAnswer != msfp.StatePending {
		t.Fatalf("A's view of B: %q %q", v.OurKey, v.RotationAnswer)
	}
	// Again (daily, or at a contact): still the one pending rotation.
	a.s.sendRotate(ctx, peer(t, a, bOnA.ID))
	if got := peer(t, b, aOnB.ID); got.Pending == nil || !msfp.SameKey(got.Pending.New, newKey) {
		t.Fatalf("after a second rotate: %+v", got.Pending)
	}
	// Meanwhile, A still signs with its old key toward B: B reads titles.
	if n, err := b.s.SyncTitles(ctx, peer(t, b, aOnB.ID)); err != nil || n != 1 {
		t.Fatalf("B reading A's titles while pending: %d, %v", n, err)
	}
	// Its operator calls ours first: a while passes.
	c.move(time.Minute)
	if _, err := b.s.ConfirmRotation(ctx, aOnB.ID); err != nil {
		t.Fatal(err)
	}
	if got := peer(t, b, aOnB.ID); !msfp.SameKey(got.PublicKey, newKey) || got.Pending != nil {
		t.Fatalf("B's view of A after confirming: %+v", got)
	}
	if _, err := b.s.ConfirmRotation(ctx, aOnB.ID); !errors.Is(err, ErrNoPending) {
		t.Fatalf("confirmed twice: %v", err)
	}
	// B's hello after confirming reaches A, which tells it again: B
	// answers rotated, and A moves.
	deadline := time.Now().Add(10 * time.Second)
	for peer(t, a, bOnA.ID).OurKey != store.OurKeyCurrent {
		if time.Now().After(deadline) {
			t.Fatal("A never learnt that B moved")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := b.s.Check(ctx, peer(t, b, aOnB.ID)); err != nil {
		t.Fatalf("B's check after: %v", err)
	}
}

// A pairing made before commitments gets them from the first hello that
// carries one (trust on first use, MSFP core section 12.5); a hello can't
// change one after.
func TestCommitmentFromHello(t *testing.T) {
	ctx := context.Background()
	a, b, bOnA, aOnB := pairedPair(t)
	for _, s := range []*server{a, b} {
		exec(t, s, `UPDATE peers SET next_commitment = NULL, commitment_from = '', commitment_told = 0`)
	}
	// A's hourly round sends B a hello carrying its commitment, once.
	a.s.Maintain(ctx)
	ab, ba := peer(t, b, aOnB.ID), peer(t, a, bOnA.ID)
	if ab.CommitmentFrom != store.CommitmentHello || !bytes.Equal(ab.NextCommitment, a.s.Keys.NextCommitment()) {
		t.Fatalf("B from A's hello: %x %q", ab.NextCommitment, ab.CommitmentFrom)
	}
	if ba.CommitmentFrom != store.CommitmentHello || !bytes.Equal(ba.NextCommitment, b.s.Keys.NextCommitment()) {
		t.Fatalf("A from B's answer: %x %q", ba.NextCommitment, ba.CommitmentFrom)
	}
	// Another commitment in a later hello: ignored.
	other := msfp.Encode64(msfp.Commitment(msfp.NewKey().Public()))
	hello := `{"state":"paired","core":[1],"parts":{"catalog":[1]},"nextCommitment":"` + other + `"}`
	if status, _ := signedCall(t, a, onlyPeer(t, a), helloPath, []byte(hello), a.s.Keys.Current()); status != http.StatusOK {
		t.Fatalf("hello: %d", status)
	}
	if got := peer(t, b, aOnB.ID); !bytes.Equal(got.NextCommitment, a.s.Keys.NextCommitment()) {
		t.Fatal("a hello replaced the commitment")
	}
	// And the rotation follows it.
	if err := a.s.Rotate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if got := peer(t, b, aOnB.ID); !msfp.SameKey(got.PublicKey, a.s.Keys.Current().Public()) {
		t.Fatal("B didn't follow the commitment it got from a hello")
	}
}

// After B made A's new key current, A's old key is still heard for 10
// minutes, requests already on the way (MSFP core section 12.4); a
// rotate repeated within them is answered rotated again and changes
// nothing. After, the old key gets the plain 404.
func TestPreviousKeyWindow(t *testing.T) {
	ctx := context.Background()
	a, b, _, aOnB := pairedPair(t)
	c := sharedClock(a, b)
	old := a.s.Keys.Current()
	next, err := a.s.nextKey(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	rot := msfp.SignRotation(old, next, nil, c.now())
	aOnBFromA := onlyPeer(t, a)
	if status, body := signedCall(t, a, aOnBFromA, rotatePath, marshal(t, rot.Body()), old); status != 200 || !strings.Contains(body, `"rotated"`) {
		t.Fatalf("rotate: %d %s", status, body)
	}
	c.move(9 * time.Minute)
	again := msfp.SignRotation(old, next, nil, c.now())
	if status, body := signedCall(t, a, aOnBFromA, rotatePath, marshal(t, again.Body()), old); status != 200 || !strings.Contains(body, `"rotated"`) {
		t.Fatalf("rotate again: %d %s", status, body)
	}
	if h, _ := b.s.Store.PeerRotations(ctx, aOnB.ID); len(h) != 1 {
		t.Fatalf("the repeat changed the history: %+v", h)
	}
	hello := []byte(`{"state":"paired","core":[1],"parts":{"catalog":[1]}}`)
	if status, _ := signedCall(t, a, aOnBFromA, helloPath, hello, old); status != 200 {
		t.Fatalf("the old key within 10 minutes: %d", status)
	}
	c.move(2 * time.Minute)
	if status, body := signedCall(t, a, aOnBFromA, helloPath, hello, old); status != 404 || body != "" && strings.Contains(body, "signature") {
		t.Fatalf("the old key after 10 minutes: %d %s", status, body)
	}
	if status, _ := signedCall(t, a, aOnBFromA, helloPath, hello, next); status != 200 {
		t.Fatalf("the new key: %d", status)
	}
}

// unreachablePeer adds to s a peer that can't be reached: a change of key
// stays under way for it.
func unreachablePeer(t *testing.T, s *server) *store.Peer {
	t.Helper()
	p, err := s.s.Store.AddPeer(context.Background(), msfp.NewKey().Public(), "https://127.0.0.1:9", "C", nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A request under A's old key that gets an unsigned 404 (B moved, but
// A never saw its answer) makes A try a hello under its new key: signed,
// so B moved, and the call goes again under it (MSFP core section 12.4).
// After 30 days, A deletes its old key, and a peer still on it is lost:
// no call is made to it, the panel says to pair again.
func TestRotationUnderWay(t *testing.T) {
	ctx := context.Background()
	a, b, bOnA, aOnB := pairedPair(t)
	c := sharedClock(a, b)
	cOnA := unreachablePeer(t, a)
	if err := a.s.Rotate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := a.s.Keys.Old(); !ok {
		t.Fatal("A dropped its old key, C hasn't moved")
	}
	// B's answer "lost": A still thinks B is on its old key.
	exec(t, a, `UPDATE peers SET our_key = 'old' WHERE id = ?`, bOnA.ID)
	c.move(11 * time.Minute)
	exec(t, a, `UPDATE peers SET rotate_sent_at = ? WHERE id = ?`, c.now().Unix(), bOnA.ID) // no rotate at this contact
	if _, err := a.s.Check(ctx, peer(t, a, bOnA.ID)); err != nil {
		t.Fatalf("A's check under the old key: %v", err)
	}
	if got := peer(t, a, bOnA.ID); got.OurKey != store.OurKeyCurrent {
		t.Fatalf("A's view of B: %q", got.OurKey)
	}
	// C is told again daily.
	sent := peer(t, a, cOnA.ID).RotateSent
	c.move(25 * time.Hour)
	a.s.Maintain(ctx)
	if got := peer(t, a, cOnA.ID).RotateSent; got == nil || !got.After(*sent) {
		t.Fatalf("C not told again: %v, %v", sent, got)
	}
	// 30 days: the old key goes, C is lost.
	c.move(KeyOverlap)
	a.s.Maintain(ctx)
	if _, _, ok := a.s.Keys.Old(); ok {
		t.Fatal("A kept its old key past 30 days")
	}
	lost := peer(t, a, cOnA.ID)
	if lost.OurKey != store.OurKeyLost {
		t.Fatalf("C: %q", lost.OurKey)
	}
	if _, err := a.s.Check(ctx, lost); !errors.Is(err, ErrKeyLost) {
		t.Fatalf("a call to C: %v", err)
	}
	if _, err := b.s.Check(ctx, peer(t, b, aOnB.ID)); err != nil {
		t.Fatalf("B still paired: %v", err)
	}
}

// B revokes its key and rotates to its next one in one action (MSFP core
// section 12.7, case 1): A suspends B, then lifts the suspension once B
// moves to the committed key.
func TestRevokeAndRotate(t *testing.T) {
	ctx := context.Background()
	a, b, bOnA, aOnB := pairedPair(t)
	revoked := b.s.Keys.Current()
	text, fingerprint, err := b.s.ExportNextKey(ctx)
	if err != nil || fingerprint != msfp.CommitmentFingerprint(b.s.Keys.NextCommitment()) {
		t.Fatalf("export: %v", err)
	}
	if err := b.s.DeleteNextKey(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.s.ExportNextKey(ctx); !errors.Is(err, ErrNoNextKey) {
		t.Fatalf("export after deleting: %v", err)
	}
	// Given back wrong: nothing happens.
	if _, err := b.s.Revoke(ctx, NextKeyText(msfp.NewKey().Seed()), false); !errors.Is(err, ErrNotNextKey) {
		t.Fatalf("another key: %v", err)
	}
	if got := peer(t, a, bOnA.ID); got.Suspended != nil {
		t.Fatal("suspended by a refused revocation")
	}
	rotated, err := b.s.Revoke(ctx, text, false)
	if err != nil || !rotated {
		t.Fatalf("revoke and rotate: %v, %v", rotated, err)
	}
	got := peer(t, a, bOnA.ID)
	if got.Suspended != nil || !msfp.SameKey(got.PublicKey, b.s.Keys.Current().Public()) || !msfp.SameKey(got.PreviousKey, revoked.Public()) {
		t.Fatalf("A's view of B: suspended %v, key moved %v", got.Suspended, msfp.SameKey(got.PublicKey, b.s.Keys.Current().Public()))
	}
	if n, err := b.s.SyncTitles(ctx, peer(t, b, aOnB.ID)); err != nil || n != 1 {
		t.Fatalf("B reading A's titles after: %d, %v", n, err)
	}
}

// A revoked key (MSFP core section 12.6): A suspends B, answers it
// nothing but rotate and revoke (the signed 404 of a forgotten key), and
// calls it no more; a rotation to another key than the committed one is
// refused, one to the committed key lifts it.
func TestSuspension(t *testing.T) {
	ctx := context.Background()
	a, b, bOnA, aOnB := pairedPair(t)
	aFromB := peer(t, b, aOnB.ID)
	key := b.s.Keys.Current()
	rev := msfp.SignRevocation(key, b.s.now())
	if status, body := signedCall(t, b, aFromB, revokePath, marshal(t, rev.Body()), key); status != 200 || !strings.Contains(body, `"suspended"`) {
		t.Fatalf("revoke: %d %s", status, body)
	}
	// Again: the same answer.
	if status, _ := signedCall(t, b, aFromB, revokePath, marshal(t, rev.Body()), key); status != 200 {
		t.Fatalf("revoke again: %d", status)
	}
	got := peer(t, a, bOnA.ID)
	if got.Suspended == nil || got.Usable() {
		t.Fatal("B not suspended")
	}
	if _, err := a.s.Check(ctx, got); !errors.Is(err, ErrSuspended) {
		t.Fatalf("a call to a suspended peer: %v", err)
	}
	if _, err := a.s.TheirLibraries(ctx, got); err == nil {
		t.Fatal("libraries of a suspended peer")
	}
	hello := []byte(`{"state":"paired","core":[1],"parts":{"catalog":[1]}}`)
	if status, _ := signedCall(t, b, aFromB, helloPath, hello, key); status != 404 {
		t.Fatalf("a hello from a revoked key: %d", status)
	}
	if status, _ := signedCall(t, b, aFromB, titlesPath, []byte(`{}`), key); status != 404 {
		t.Fatalf("titles from a revoked key: %d", status)
	}
	// Its titles are no longer shown here.
	if n, _ := a.s.Store.PeerTitleCounts(ctx); n[bOnA.ID] != 0 {
		t.Fatalf("titles of B: %d", n[bOnA.ID])
	}
	next, err := b.s.nextKey(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	// To another key than the committed one: refused.
	wrong := msfp.SignRotation(key, msfp.NewKey(), nil, b.s.now())
	if status, _ := signedCall(t, b, aFromB, rotatePath, marshal(t, wrong.Body()), key); status != http.StatusConflict {
		t.Fatalf("a rotation to another key: %d", status)
	}
	if got := peer(t, a, bOnA.ID); got.Suspended == nil {
		t.Fatal("lifted by a refused rotation")
	}
	// To the committed one: lifted.
	right := msfp.SignRotation(key, next, nil, b.s.now())
	if status, body := signedCall(t, b, aFromB, rotatePath, marshal(t, right.Body()), key); status != 200 {
		t.Fatalf("a rotation to the committed key: %d %s", status, body)
	}
	if got := peer(t, a, bOnA.ID); got.Suspended != nil || !msfp.SameKey(got.PublicKey, next.Public()) {
		t.Fatal("not lifted")
	}
}

// A peer suspended 30 days without a valid rotation becomes gone, and
// stays suspended: its key gets nothing back (MSFP core section 12.6).
// That's what a server that revoked without its next key leaves behind:
// it ends every pairing, to make them again (section 12.7, case 2).
func TestSuspendedGoneAfter30Days(t *testing.T) {
	ctx := context.Background()
	a, b, bOnA, _ := pairedPair(t)
	c := sharedClock(a, b)
	if err := b.s.DeleteNextKey(ctx); err != nil {
		t.Fatal(err)
	}
	revoked := b.s.Keys.Current()
	rotated, err := b.s.Revoke(ctx, "", false)
	if err != nil || rotated {
		t.Fatalf("revoke without the next key: %v, %v", rotated, err)
	}
	noPeers(t, b)
	if msfp.SameKey(b.s.Keys.Current().Public(), revoked.Public()) {
		t.Fatal("B kept its revoked key")
	}
	if _, onServer, _ := b.s.NextKey(ctx); !onServer {
		t.Fatal("B has no new next key")
	}
	if got := peer(t, a, bOnA.ID); got.Suspended == nil {
		t.Fatal("A didn't suspend B")
	}
	c.move(KeyOverlap - time.Hour)
	a.s.Maintain(ctx)
	if got := peer(t, a, bOnA.ID); got.State != store.PeerPaired {
		t.Fatalf("gone before 30 days: %s", got.State)
	}
	c.move(time.Hour)
	a.s.Maintain(ctx)
	if got := peer(t, a, bOnA.ID); got.State != store.PeerGone || got.Suspended == nil {
		t.Fatalf("after 30 days: %s, suspended %v", got.State, got.Suspended)
	}
	// The pairing's end remembers the revoked key as forgotten, with its
	// history.
	if err := a.s.End(ctx, bOnA.ID); err != nil {
		t.Fatal(err)
	}
	if f, _ := a.s.Store.PeerForgotten(ctx, revoked.Public()); !f {
		t.Fatal("the revoked key isn't forgotten")
	}
}

// A rotate's answers are what MSFP core section 9.3 says, signed.
func TestRotateAnswerSigned(t *testing.T) {
	a, _, _, _ := pairedPair(t)
	aOnBFromA := onlyPeer(t, a)
	rot := msfp.SignRotation(a.s.Keys.Current(), msfp.NewKey(), nil, a.s.now())
	var out json.RawMessage
	r, err := a.s.callOnce(context.Background(), aOnBFromA, rotatePath, rot.Body(), &out, a.s.Keys.Current())
	if err != nil || r.Status != http.StatusConflict || r.Problem != "rotation-refused" {
		t.Fatalf("%+v, %v", r, err)
	}
}

// Revoking without moving to the next key, though its seed is on the
// server (it may be stolen with the server's storage): a new key, every
// pairing ended (MSFP core section 12.7, case 2).
func TestRevokeWithoutNextKey(t *testing.T) {
	ctx := context.Background()
	a, b, bOnA, _ := pairedPair(t)
	next, err := b.s.nextKey(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := b.s.Revoke(ctx, "", true)
	if err != nil || rotated {
		t.Fatalf("%v, %v", rotated, err)
	}
	noPeers(t, b)
	if msfp.SameKey(b.s.Keys.Current().Public(), next.Public()) {
		t.Fatal("B moved to the next key it was told not to")
	}
	if got := peer(t, a, bOnA.ID); got.Suspended == nil {
		t.Fatal("A didn't suspend B")
	}
}

// What the end of a change of key, or a peer's move, learnt late does
// once the next change began: nothing. Here in the order that went wrong
// (a hello, or the hourly round, seen first, its effect last): A changes
// its key once, C never moving; our operator changes it again (C lost,
// D now on the old key); then the first change's end, and a move to its
// key, arrive. Neither may delete the second change's old key, nor mark
// D moved or lost.
func TestLateEndOfChange(t *testing.T) {
	ctx := context.Background()
	a, _, bOnA, _ := pairedPair(t)
	cOnA := unreachablePeer(t, a)
	if err := a.s.Rotate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	first, _, ok := a.s.Keys.Old()
	if !ok {
		t.Fatal("A dropped its old key, C hasn't moved")
	}
	firstNew := a.s.Keys.Current()
	dOnA := unreachablePeer(t, a)
	if err := a.s.Rotate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	second, _, ok := a.s.Keys.Old()
	if !ok || !msfp.SameKey(second.Public(), firstNew.Public()) {
		t.Fatal("A's second change isn't under way")
	}
	if peer(t, a, cOnA.ID).OurKey != store.OurKeyLost || peer(t, a, bOnA.ID).OurKey != store.OurKeyCurrent {
		t.Fatal("the second change didn't end the first")
	}
	// D "moved" to the first change's new key: the old key now.
	a.s.peerMoved(ctx, peer(t, a, dOnA.ID), firstNew.Public())
	if got := peer(t, a, dOnA.ID); got.OurKey != store.OurKeyOld {
		t.Fatalf("D after a late move: %q", got.OurKey)
	}
	// The first change's end, from a peer's last move or the hourly round.
	if err := a.s.finishRotation(ctx, first.Public()); err != nil {
		t.Fatal(err)
	}
	if old, _, ok := a.s.Keys.Old(); !ok || !msfp.SameKey(old.Public(), second.Public()) {
		t.Fatal("a late end deleted the second change's old key")
	}
	if stored, err := a.s.Store.FederationKeys(ctx); err != nil || stored.Old == nil {
		t.Fatalf("the stored old key after a late end: %v", err)
	}
	if got := peer(t, a, dOnA.ID); got.OurKey != store.OurKeyOld {
		t.Fatalf("D after a late end: %q", got.OurKey)
	}
	// D moving to the key of now ends the second change.
	a.s.peerMoved(ctx, peer(t, a, dOnA.ID), a.s.Keys.Current().Public())
	if got := peer(t, a, dOnA.ID); got.OurKey != store.OurKeyCurrent {
		t.Fatalf("D after its move: %q", got.OurKey)
	}
	if _, _, ok := a.s.Keys.Old(); ok {
		t.Fatal("A kept its old key, every peer moved")
	}
}
