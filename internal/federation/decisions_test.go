package federation

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/msfp"
	"github.com/overfin/server/internal/store"
)

// MSFP version 1 as decided on 2026-11-18 (core section 18, items 37 to
// 40): what each decision changed here.

// Both sides of pair commit to their next key (MSFP core section 9.1): a
// request without its commitment is a bad body, the plain 404, and the
// code stays; an answer without one fails like a badly signed one, and
// the redeemer keeps nothing.
func TestPairNeedsCommitments(t *testing.T) {
	ctx := context.Background()
	a, b := newServer(t, "A"), newServer(t, "B")
	text, _, err := a.s.NewCode(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	c, err := msfp.ParseCode(text)
	if err != nil {
		t.Fatal(err)
	}
	key := b.s.Keys.Current()
	req := pairRequest{Address: b.url, Name: "B", SecretHash: msfp.Encode64(store.PairingSecretHash(c.Secret)),
		Proof: msfp.PairingProof(c.Secret, key.Public(), b.url)}
	status, _ := signedCall(t, b, &store.Peer{Address: a.url}, pairPath, marshal(t, req), key)
	if status != http.StatusNotFound {
		t.Fatalf("a pair without nextCommitment: %d", status)
	}
	noPeers(t, a)

	// A's answer without its commitment (as if its next key were gone):
	// B keeps nothing, and says why.
	a.s.Keys.mu.Lock()
	next := a.s.Keys.next
	a.s.Keys.next = nil
	a.s.Keys.mu.Unlock()
	if _, err := b.s.UseCode(ctx, text); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("a pair answer without nextCommitment: %v", err)
	}
	noPeers(t, b)
	a.s.Keys.mu.Lock()
	a.s.Keys.next = next
	a.s.Keys.mu.Unlock()
}

// A retired key never becomes current again (MSFP core section 12.3,
// step 1): a rotation back to the key a peer moved from, or to any of its
// former keys, is refused, even where it would otherwise wait for the
// operator.
func TestRotationBackToFormerKey(t *testing.T) {
	ctx := context.Background()
	a, b, _, aOnB := pairedPair(t)
	first := a.s.Keys.Current()
	if err := a.s.Rotate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	second := a.s.Keys.Current()
	if got := peer(t, b, aOnB.ID); !msfp.SameKey(got.PublicKey, second.Public()) {
		t.Fatal("B didn't follow A's change of key")
	}
	// No commitment held: any other key would wait for B's operator.
	exec(t, b, `UPDATE peers SET next_commitment = NULL, commitment_from = ''`)
	toB := onlyPeer(t, a)
	back := msfp.SignRotation(second, first, nil, a.s.now())
	if status, body := signedCall(t, a, toB, rotatePath, marshal(t, back.Body()), second); status != http.StatusConflict ||
		!strings.Contains(body, "rotation-refused") {
		t.Fatalf("a rotation back to the former key: %d %s", status, body)
	}
	// After the previous key's 10 minutes, dropped: still refused, from
	// the history.
	exec(t, b, `UPDATE peers SET previous_key = NULL, previous_until = NULL`)
	back = msfp.SignRotation(second, first, nil, a.s.now())
	if status, _ := signedCall(t, a, toB, rotatePath, marshal(t, back.Body()), second); status != http.StatusConflict {
		t.Fatalf("a rotation back to a key of the history: %d", status)
	}
	other := msfp.SignRotation(second, msfp.NewKey(), nil, a.s.now())
	if status, _ := signedCall(t, a, toB, rotatePath, marshal(t, other.Body()), second); status != http.StatusAccepted {
		t.Fatalf("a rotation to a new key: %d", status)
	}
}

// Ending a pairing forgets every key of the peer, a pending rotation's
// key included (MSFP core section 7.4, step 1).
func TestEndForgetsPendingKey(t *testing.T) {
	ctx := context.Background()
	a, b, _, aOnB := pairedPair(t)
	exec(t, b, `UPDATE peers SET next_commitment = NULL, commitment_from = ''`)
	pending := msfp.NewKey()
	rot := msfp.SignRotation(a.s.Keys.Current(), pending, nil, a.s.now())
	if status, _ := signedCall(t, a, onlyPeer(t, a), rotatePath, marshal(t, rot.Body()), a.s.Keys.Current()); status != http.StatusAccepted {
		t.Fatalf("rotate: %d", status)
	}
	if err := b.s.End(ctx, aOnB.ID); err != nil {
		t.Fatal(err)
	}
	if f, err := b.s.Store.PeerForgotten(ctx, pending.Public()); err != nil || !f {
		t.Fatalf("the pending key forgotten: %v %v", f, err)
	}
}

// A revoked key is never heard again (MSFP core section 12.6): when the
// rotation to the committed key lifts the suspension, the revoked key
// gets no 10 minutes as a previous key.
func TestRevokedKeyNotHeardAfterRotation(t *testing.T) {
	ctx := context.Background()
	a, b, _, aOnB := pairedPair(t)
	revoked := a.s.Keys.Current()
	if rotated, err := a.s.Revoke(ctx, "", false); err != nil || !rotated {
		t.Fatalf("revoke and rotate: %v %v", rotated, err)
	}
	got := peer(t, b, aOnB.ID)
	if got.Suspended != nil || !msfp.SameKey(got.PublicKey, a.s.Keys.Current().Public()) {
		t.Fatal("B didn't follow A to its committed key")
	}
	hello := []byte(`{"state":"paired","core":[1],"parts":{"catalog":[1]}}`)
	if status, _ := signedCall(t, a, onlyPeer(t, a), helloPath, hello, revoked); status != http.StatusNotFound {
		t.Fatalf("the revoked key heard after the rotation: %d", status)
	}
	if status, _ := signedCall(t, a, onlyPeer(t, a), helloPath, hello, a.s.Keys.Current()); status != http.StatusOK {
		t.Fatalf("the new key: %d", status)
	}
}

// A key stolen with no next key (MSFP core section 12.7, case 2): every
// pairing ends, no ended is sent, and for 30 days a former peer's hello
// gets a 200 signed with the revoked key, carrying only the revocation,
// newly dated. A peer that revoke didn't reach suspends the pairing on
// reading it, whether it can be called or not.
func TestRevocationAnswersAfterNewKey(t *testing.T) {
	ctx := context.Background()
	a, b, bOnA, _ := pairedPair(t)
	c := sharedClock(a, b)
	revoked := b.s.Keys.Current()
	if rotated, err := b.s.Revoke(ctx, "", true); err != nil || rotated {
		t.Fatalf("revoke without the next key: %v %v", rotated, err)
	}
	noPeers(t, b)
	hello := marshal(t, a.s.ourHello(ctx, peer(t, a, bOnA.ID), helloPaired, false))
	ask := func() (int, http.Header, []byte, string) {
		t.Helper()
		req, _ := http.NewRequest("POST", b.url+helloPath, bytes.NewReader(hello))
		req.Header.Set("Content-Type", "application/json")
		nonce := msfp.SignRequest(req, hello, a.s.Keys.Current(), c.now())
		res, err := a.s.conns(&store.Peer{Address: b.url}, true).client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header, body, nonce
	}
	status, h, body, nonce := ask()
	if status != http.StatusOK || msfp.CheckResponse(revoked.Public(), nonce, status, "application/json", body, h.Get(msfp.HeaderSignature)) != nil {
		t.Fatalf("a former peer's hello: %d %s, signed with the revoked key: %v", status, body,
			msfp.CheckResponse(revoked.Public(), nonce, status, "application/json", body, h.Get(msfp.HeaderSignature)))
	}
	var only map[string]json.RawMessage
	if err := json.Unmarshal(body, &only); err != nil || len(only) != 1 || only["revocation"] == nil {
		t.Fatalf("the answer holds more than the revocation: %s", body)
	}
	if _, err := msfp.ReadRevoke(only["revocation"], revoked.Public(), c.now()); err != nil {
		t.Fatalf("the revocation: %v", err)
	}
	// A, whose suspension of B is undone as if revoke never reached it,
	// suspends B on its next hello's answer.
	// Nothing else of that answer is kept: B's versions stay.
	exec(t, a, `UPDATE peers SET suspended_at = NULL`)
	versions := peer(t, a, bOnA.ID).Versions
	if _, err := a.s.Check(ctx, peer(t, a, bOnA.ID)); err != nil {
		t.Fatal(err)
	}
	if got := peer(t, a, bOnA.ID); got.Suspended == nil || got.Versions != versions {
		t.Fatalf("A's view of B after the answer: suspended %v, versions %q, were %q", got.Suspended, got.Versions, versions)
	}
	// After 30 days: the revoked key gone, the signed 404 of a forgotten
	// key, under B's new key.
	c.move(KeyOverlap + time.Minute)
	b.s.Maintain(ctx)
	if _, _, ok := b.s.Keys.Revoked(); ok {
		t.Fatal("the revoked key kept past its 30 days")
	}
	status, h, body, nonce = ask()
	if status != http.StatusNotFound || msfp.CheckResponse(b.s.Keys.Current().Public(), nonce, status, h.Get("Content-Type"), body, h.Get(msfp.HeaderSignature)) != nil {
		t.Fatalf("a former peer's hello after 30 days: %d %s", status, body)
	}
}

// With no core in common (MSFP core sections 10.3 and 10.4), a server
// answers every call but hello with a signed 404 not-supported, and makes
// no call but hello.
func TestNoCommonCore(t *testing.T) {
	ctx := context.Background()
	a, b, bOnA, aOnB := pairedPair(t)
	exec(t, a, `UPDATE peers SET versions = '{"core":[2],"parts":{}}'`)
	if _, err := a.s.TheirLibraries(ctx, peer(t, a, bOnA.ID)); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("a call to a peer with no common core: %v", err)
	}
	if _, err := a.s.call(ctx, peer(t, a, bOnA.ID), revokePath, struct{}{}, &struct{}{}); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("a revoke to a peer with no common core: %v", err)
	}
	// B, told A speaks core 2 only: A's rotate is not-supported, signed.
	exec(t, b, `UPDATE peers SET versions = '{"core":[2],"parts":{}}'`)
	rot := msfp.SignRotation(a.s.Keys.Current(), msfp.NewKey(), nil, a.s.now())
	if status, body := signedCall(t, a, onlyPeer(t, a), rotatePath, marshal(t, rot.Body()), a.s.Keys.Current()); status != http.StatusNotFound ||
		!strings.Contains(body, "not-supported") {
		t.Fatalf("a rotate with no common core: %d %s", status, body)
	}
	if got := peer(t, b, aOnB.ID); got.Pending != nil || got.NextCommitment == nil {
		t.Fatal("a rotate with no common core changed something")
	}
	// hello is still answered, and puts things right.
	if _, err := a.s.Check(ctx, peer(t, a, bOnA.ID)); err != nil {
		t.Fatalf("hello with no common core: %v", err)
	}
	if _, err := a.s.TheirLibraries(ctx, peer(t, a, bOnA.ID)); err != nil {
		t.Fatalf("libraries after a hello: %v", err)
	}
}

// During a move, a server names in its hellos the pins of both its
// addresses, the old and the new, until the move ends (MSFP core section
// 3.2).
func TestPinsOfBothAddressesWhileMoving(t *testing.T) {
	ctx := context.Background()
	serve := func() (string, string) {
		t.Helper()
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "127.0.0.1"},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		cert, _ := x509.ParseCertificate(der)
		ts := httptest.NewUnstartedServer(http.NotFoundHandler())
		ts.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
		ts.StartTLS()
		t.Cleanup(ts.Close)
		return ts.URL, msfp.Pin(cert)
	}
	now, nowPin := serve()
	former, formerPin := serve()
	s := &Service{Address: now, Certificates: &Certificates{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if got := s.ownCertificates(ctx, false); !slices.Equal(got, []string{nowPin}) {
		t.Fatalf("no move: %v", got)
	}
	s.former, s.movedAt = former, time.Now()
	want := msfp.CleanPins([]string{nowPin, formerPin})
	if got := s.ownCertificates(ctx, true); !slices.Equal(got, want) {
		t.Fatalf("moving: %v, want %v", got, want)
	}
	s.former = ""
	if got := s.ownCertificates(ctx, false); !slices.Equal(got, []string{nowPin}) {
		t.Fatalf("the move over: %v", got)
	}
}
