package msfp_test

import (
	"bytes"
	"context"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/overfin/server/msfp"
	"github.com/overfin/server/msfp/wire"
)

// U can't be called (MSFP core section 7.5): it redeems A's code, is
// paired by its scheduled hellos, follows A's rotation and A's revoked
// key from A's answers, and no connection ever reaches it, until it
// can be called again: A then confirms its address by a hello there,
// and calls it.
func TestUncallableServer(t *testing.T) {
	ctx := context.Background()
	clock := newClock(1800000000)
	var open atomic.Bool
	u := newServerWith(t, msfp.Config{Clock: clock}, serverOpts{conn: func() {
		if !open.Load() {
			t.Error("a connection reached the server that can't be called")
		}
	}})
	a := newServer(t, msfp.Config{Clock: clock})
	if err := u.eng.SetUncallable(ctx, true); err != nil {
		t.Fatal(err)
	}
	sv, _ := u.eng.Server(ctx)
	if !sv.Uncallable || !wire.Uncallable(sv.Address) || sv.Named != sv.Address || sv.Former != "" || !sv.MovedAt.IsZero() {
		t.Fatalf("U: %+v", sv)
	}
	invalid := sv.Address
	if _, _, err := u.eng.MakeCode(ctx, ""); !errors.Is(err, msfp.ErrNoCodes) {
		t.Errorf("U's code: %v", err)
	}

	// Pairing: U redeems A's code; A accepts, and can't tell U; U's
	// scheduled hello learns it.
	code, _, _ := a.eng.MakeCode(ctx, "")
	aOnU, err := u.eng.RedeemCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	uOnA := onlyPeer(t, a.eng)
	if uOnA.Inbound != msfp.InboundNo || uOnA.Address != invalid {
		t.Fatalf("U on A: %+v", uOnA)
	}
	if _, err := a.eng.Accept(ctx, uOnA.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.eng.Check(ctx, uOnA.ID); !errors.Is(err, msfp.ErrUncallable) {
		t.Errorf("A to U: %v", err)
	}
	if err := u.eng.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if p, _ := u.eng.Peer(ctx, aOnU.ID); p.State != msfp.Paired {
		t.Fatalf("A on U: %+v", p)
	}
	hello := a.exchanges("hello")
	if len(hello) == 0 || !strings.Contains(hello[len(hello)-1].reqBody, `"inbound":false,"knownKey":"`) {
		t.Errorf("U's hello: %+v", hello)
	}
	// Not due again for 5 minutes, give or take a fifth.
	n := len(a.exchanges("hello"))
	clock.Add(3 * time.Minute)
	u.eng.RunOnce(ctx)
	if len(a.exchanges("hello")) != n {
		t.Error("a hello before its turn")
	}

	// A rotates: no rotate to U; U pulls it from A's next answer, then
	// says it knows A's new key, and A's change is over.
	if err := a.eng.Rotate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	clock.Add(3 * time.Minute)
	u.eng.RunOnce(ctx)
	if _, ok := has[msfp.PeerRotated](u.events); !ok {
		t.Fatal("U didn't follow A's rotation")
	}
	u.eng.RunOnce(ctx) // the hello sent at once, knownKey the new key
	if sv, _ := a.eng.Server(ctx); !sv.RotationSince.IsZero() {
		t.Errorf("A's change not over: %+v", sv)
	}
	if aSv, _ := a.eng.Server(ctx); peerFingerprint(t, u, aOnU.ID) != aSv.Fingerprint {
		t.Errorf("A on U: %s, A is %s", peerFingerprint(t, u, aOnU.ID), aSv.Fingerprint)
	}

	// A's key stolen: revoked, and A rotates to its next one. U, pulling
	// both, suspends A, then follows the rotation it had committed to.
	if _, err := a.eng.KeyStolen(ctx, "", false); err != nil {
		t.Fatal(err)
	}
	clock.Add(6 * time.Minute)
	u.eng.RunOnce(ctx)
	if _, ok := has[msfp.PeerSuspended](u.events); !ok {
		t.Error("U didn't suspend A")
	}
	if _, ok := has[msfp.PeerResumed](u.events); !ok {
		t.Error("U didn't follow A's rotation after the revocation")
	}
	u.eng.RunOnce(ctx)
	aView, _ := a.eng.Server(ctx)
	if p, _ := u.eng.Peer(ctx, aOnU.ID); !p.Suspended.IsZero() || p.Fingerprint != aView.Fingerprint {
		t.Errorf("A on U: %+v, A is %s", p, aView.Fingerprint)
	}
	if !aView.RotationSince.IsZero() {
		t.Errorf("A's change not over: %+v", aView)
	}

	// U can be called again: it reaches itself, tells A, and A confirms
	// U's address by a hello there before calling it.
	open.Store(true)
	if err := u.eng.SetUncallable(ctx, false); err != nil {
		t.Fatal(err)
	}
	waitAll(a, u)
	if sv, _ := u.eng.Server(ctx); sv.Uncallable || sv.Address != u.srv.URL || sv.Former != "" {
		t.Errorf("U: %+v", sv)
	}
	if ev, ok := has[msfp.InboundChanged](a.events); !ok || ev.Inbound != msfp.InboundYes {
		t.Errorf("A's InboundChanged: %+v", ev)
	}
	if p, _ := a.eng.Peer(ctx, uOnA.ID); p.Inbound != msfp.InboundYes || p.Address != u.srv.URL {
		t.Errorf("U on A: %+v", p)
	}
	if _, err := a.eng.Check(ctx, uOnA.ID); err != nil {
		t.Errorf("A to U: %v", err)
	}

	// And can't again: the same .invalid address, no former, A stores it
	// at once from U's hello.
	if err := u.eng.SetUncallable(ctx, true); err != nil {
		t.Fatal(err)
	}
	open.Store(false)
	if sv, _ := u.eng.Server(ctx); sv.Address != invalid || sv.Former != "" || !sv.MovedAt.IsZero() {
		t.Errorf("U: %+v", sv)
	}
	u.eng.RunOnce(ctx)
	if p, _ := a.eng.Peer(ctx, uOnA.ID); p.Inbound != msfp.InboundNo || p.Address != invalid || p.Moving != nil {
		t.Errorf("U on A: %+v", p)
	}
	if ev, ok := has[msfp.PeerMoved](a.events); !ok || ev.To != invalid {
		t.Errorf("A's PeerMoved: %+v", ev)
	}
}

// The addresses of strict.json's uncallableAddresses, in hellos saying
// inbound false, through an engine: each valid one is stored at once,
// each invalid one refused, signed, and nothing kept (MSFP core sections
// 5.2 and 9.2).
func TestUncallableAddressVectorsThroughEngine(t *testing.T) {
	var cases struct {
		UncallableAddresses []struct {
			Text   string
			Valid  bool
			Reason string
		}
	}
	b, err := os.ReadFile("wire/testdata/v1/strict.json")
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(b, &cases)
	v := newVectorPair(t)
	keyB, err := wire.KeyFromSeed(mustHex(t, v.keys["b"].Seed))
	if err != nil {
		t.Fatal(err)
	}
	stored := v.onA.Address
	for _, c := range cases.UncallableAddresses {
		body, _ := json.Marshal(map[string]any{"state": "paired", "core": []int{1}, "address": c.Text, "inbound": false})
		r := httptest.NewRequest("POST", "https://a.example.org/api/v1/federation/hello", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		wire.SignRequest(r, body, keyB, v.clockA.Now())
		w := httptest.NewRecorder()
		v.a.eng.FederationHandler().ServeHTTP(w, r)
		got := peerRecord(t, v.storeA, v.onA.ID)
		switch {
		case c.Valid && (w.Code != 200 || got.Address != c.Text || got.Inbound != msfp.InboundNo):
			t.Errorf("%q: %d %s, B at %s", c.Text, w.Code, w.Body, got.Address)
		case !c.Valid && (w.Code != 404 || w.Header().Get("MSFP-Signature") == "" || got.Address != stored):
			t.Errorf("%q (%s): %d, B at %s", c.Text, c.Reason, w.Code, got.Address)
		}
		stored = got.Address
	}
}

// The last three exchanges of exchanges.json from B's side, through its
// engine: B can't be called, at the vectors' address; its scheduled
// hellos carry inbound false and knownKey a; A's answer with a rotation
// and a revocation makes B suspend a, then move to a2; B's next hello,
// at once, carries knownKey a2, and A's answer under a2 is B's to keep.
func TestUncallableExchangesFromCaller(t *testing.T) {
	ctx := context.Background()
	v := newVectorPair(t)
	addr, err := base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding).
		DecodeString("4dq6fy7e4xtop2hj5lv6z3po54")
	if err != nil {
		t.Fatal(err)
	}
	v.randB.queue(hex.EncodeToString(addr))
	if err := v.b.eng.SetUncallable(ctx, true); err != nil {
		t.Fatal(err)
	}
	if sv, _ := v.b.eng.Server(ctx); sv.Address != "https://msfp-4dq6fy7e4xtop2hj5lv6z3po54.invalid" {
		t.Fatalf("B: %+v", sv)
	}

	var mu sync.Mutex
	var answering vector
	var asked []string
	fake := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		vec := answering
		asked = append(asked, string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", vec.AnswerContentType)
		w.Header().Set("MSFP-Signature", vec.AnswerSignature)
		w.WriteHeader(vec.Status)
		w.Write([]byte(vec.AnswerBody))
	}))
	defer fake.Close()
	v.dialB.set(hostOf(fake))
	v.a.srv.CloseClientConnections() // B's connection from the pair, to the real A
	send := func(name string, at int64) map[string]any {
		t.Helper()
		vec := v.vs[name]
		mu.Lock()
		answering, asked = vec, nil
		mu.Unlock()
		v.clockB.Set(at)
		v.randB.queue(vec.Nonce)
		if err := v.b.eng.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(asked) != 1 {
			t.Fatalf("%s: B sent %d hellos", name, len(asked))
		}
		var got, want map[string]any
		json.Unmarshal([]byte(asked[0]), &got)
		json.Unmarshal([]byte(vec.Body), &want)
		for _, m := range []string{"address", "inbound", "knownKey"} {
			if got[m] != want[m] {
				t.Errorf("%s: B's %s %v, want %v", name, m, got[m], want[m])
			}
		}
		return got
	}

	send("hello, from a server that can't be called", 1800000240)
	rec := peerRecord(t, v.storeB, v.onB.ID)
	if rec.State != msfp.Paired || rec.Seen.Unix() != 1800000240 || len(rec.Pins) != 1 {
		t.Errorf("A on B: %+v", rec)
	}
	send("hello, answered with a rotation and a revocation", 1800300000)
	rec = peerRecord(t, v.storeB, v.onB.ID)
	if !bytes.Equal(rec.Key, mustKey(t, v.keys["a2"].Public)) || !rec.Suspended.IsZero() {
		t.Errorf("A on B: %+v", rec)
	}
	for _, want := range []string{"PeerSuspended", "PeerRotated", "PeerResumed"} {
		found := false
		for _, ev := range v.b.events.all() {
			found = found || strings.HasSuffix(typeName(ev), want)
		}
		if !found {
			t.Errorf("no %s on B", want)
		}
	}
	send("hello, with the new key known", 1800300060)
	if rec := peerRecord(t, v.storeB, v.onB.ID); rec.Seen.Unix() != 1800300060 {
		t.Errorf("A's answer under a2 not kept: %+v", rec)
	}
}

func peerFingerprint(t *testing.T, s *server, id string) string {
	t.Helper()
	p, err := s.eng.Peer(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p.Fingerprint
}
