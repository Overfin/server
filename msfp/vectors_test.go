package msfp_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/overfin/server/msfp"
	"github.com/overfin/server/msfp/store/memory"
	"github.com/overfin/server/msfp/wire"
)

// The vectors of MSFP core section 17 (vectors/v1/, copied in
// wire/testdata/v1/), through two engines: their pair exchange byte for
// byte both ways, the hello and play requests a peer receives, and the
// hello answers a caller reads.

type vector struct {
	Name, Signer, Answerer, Method, Host, Target, Date, Nonce, NonceText string
	Body, Signature                                                      string
	Status                                                               int
	AnswerContentType, AnswerBody, AnswerSignature                       string
}

func readVectors(t *testing.T) (map[string]vector, map[string]struct{ Seed, Public string }, string) {
	t.Helper()
	var ex struct{ Exchanges []vector }
	var keys struct {
		Keys map[string]struct{ Seed, Public string }
	}
	var code struct{ Code struct{ Secret, Text string } }
	for name, v := range map[string]any{"exchanges.json": &ex, "keys.json": &keys, "code.json": &code} {
		b, err := os.ReadFile("wire/testdata/v1/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatal(err)
		}
	}
	byName := map[string]vector{}
	for _, v := range ex.Exchanges {
		byName[v.Name] = v
	}
	return byName, keys.Keys, code.Code.Text
}

const ids = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"

// vectorPair is the vectors' two servers, A at a.example.org and B at
// 192.0.2.10:8096, paired as the pair vector says (B redeemed A's code:
// A is theirs on B, B ours on A), each on its own clock at 1800000000.
type vectorPair struct {
	vs             map[string]vector
	keys           map[string]struct{ Seed, Public string }
	clockA, clockB *fakeClock
	randA, randB   *script
	dialA, dialB   *routeDialer
	storeA, storeB msfp.Store
	a, b           *server
	onA, onB       msfp.PeerView
}

func newVectorPair(t *testing.T) *vectorPair {
	t.Helper()
	ctx := context.Background()
	vs, keys, codeText := readVectors(t)
	v := &vectorPair{vs: vs, keys: keys, clockA: newClock(1800000000), clockB: newClock(1800000000),
		randA: &script{}, randB: &script{}, dialA: &routeDialer{}, dialB: &routeDialer{},
		storeA: memory.New(), storeB: memory.New()}
	v.randA.queue(keys["a"].Seed, keys["a2"].Seed, ids, "404142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f")
	v.randB.queue(keys["b"].Seed, keys["b2"].Seed, ids, vs["pair"].Nonce)
	v.a = newServer(t, msfp.Config{Address: "https://a.example.org", Name: func() string { return "Server A" },
		Store: v.storeA, Clock: v.clockA, Random: v.randA, Dialer: v.dialA})
	v.b = newServer(t, msfp.Config{Address: "https://192.0.2.10:8096", Name: func() string { return "Server B" },
		Store: v.storeB, Clock: v.clockB, Random: v.randB, Dialer: v.dialB})

	// pair, B to A: the code A makes is the vector's; B's request and A's
	// answer are the vector's, byte for byte.
	code, _, err := v.a.eng.MakeCode(ctx, "")
	if err != nil || code != codeText {
		t.Fatalf("A's code %q, %v", code, err)
	}
	v.dialB.set(v.a.srv.Listener.Addr().String())
	v.onB, err = v.b.eng.RedeemCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	got, want := v.a.lastExchange(), vs["pair"]
	if got.reqBody != want.Body || got.reqSig != want.Signature {
		t.Errorf("B's pair request:\n%s %s\nwant\n%s %s", got.reqBody, got.reqSig, want.Body, want.Signature)
	}
	if got.status != 200 || got.resBody != want.AnswerBody || got.resSig != want.AnswerSignature {
		t.Errorf("A's pair answer:\n%d %s %s\nwant\n%s %s", got.status, got.resBody, got.resSig, want.AnswerBody, want.AnswerSignature)
	}
	v.onA = onlyPeer(t, v.a.eng)
	return v
}

func (v *vectorPair) setClocks(unix int64) {
	v.clockA.Set(unix)
	v.clockB.Set(unix)
}

// pins has B send A a hello, so that A knows the pin of B's certificate,
// which its calls but pair and hello need (MSFP core section 3.2).
func (v *vectorPair) pins(t *testing.T) {
	t.Helper()
	if _, err := v.b.eng.Check(context.Background(), v.onB.ID); err != nil {
		t.Fatal(err)
	}
	v.dialA.set(v.b.srv.Listener.Addr().String())
}

func TestVectorsThroughEngines(t *testing.T) {
	ctx := context.Background()
	v := newVectorPair(t)
	vs, keys := v.vs, v.keys
	clockB, randA, dialA := v.clockB, v.randA, v.dialA
	storeA, storeB := v.storeA, v.storeB
	a, b, onA, onB := v.a, v.b, v.onA, v.onB
	if onB.State != msfp.Theirs || onB.Name != "Server A" || onB.Address != "https://a.example.org" {
		t.Errorf("A on B: %+v", onB)
	}
	if onA.State != msfp.Ours || onA.Name != "Server B" || onA.Address != "https://192.0.2.10:8096" {
		t.Errorf("B on A: %+v", onA)
	}

	// hello, A to B: B, waiting for A's operator, is paired by it, keeps
	// A's versions and pins, and answers signed with b.
	clockB.Set(1800000060)
	res := serveVector(t, b.eng, vs["hello"])
	if res.Code != http.StatusOK || wire.CheckResponse(mustKey(t, keys["b"].Public), vs["hello"].NonceText, res.Code,
		res.Header().Get("Content-Type"), res.Body.Bytes(), res.Header().Get("MSFP-Signature")) != nil {
		t.Fatalf("B's hello answer: %d %s", res.Code, res.Body)
	}
	if !strings.HasPrefix(res.Body.String(), `{"state":"paired","core":[1],"parts":{}`) {
		t.Errorf("B's hello answer: %s", res.Body)
	}
	recB := peerRecord(t, storeB, onB.ID)
	wantVersions := wire.Versions{Core: []int{1}, Parts: map[string][]int{"catalog": {1}, "playback": {1}, "media": {1}, "video": {1}}}
	if recB.State != msfp.Paired || !reflect.DeepEqual(*recB.Versions, wantVersions) ||
		!reflect.DeepEqual(recB.Pins, []string{"JN-hf1COiASAAUsiYeHXMNVvNnxYR2dKYnBT2Zx_6Fo"}) {
		t.Errorf("A on B: %+v", recB)
	}

	// play, A to B: B speaks no playback, so a signed 404 not-supported,
	// byte for byte.
	clockB.Set(1800000180)
	res = serveVector(t, b.eng, vs["play, to a peer without playback"])
	want := vs["play, to a peer without playback"]
	if res.Code != 404 || res.Body.String() != want.AnswerBody || res.Header().Get("MSFP-Signature") != want.AnswerSignature {
		t.Errorf("B's play answer: %d %q %s", res.Code, res.Body, res.Header().Get("MSFP-Signature"))
	}

	// The hello answers A reads: B's, signed with b, whose versions and
	// pins A keeps; then B's signed 404, and A marks B gone.
	var mu sync.Mutex
	var answering vector
	fake := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		v := answering
		mu.Unlock()
		w.Header().Set("Content-Type", v.AnswerContentType)
		w.Header().Set("MSFP-Signature", v.AnswerSignature)
		w.WriteHeader(v.Status)
		w.Write([]byte(v.AnswerBody))
	}))
	defer fake.Close()
	dialA.set(fake.Listener.Addr().String())
	answerWith := func(v vector) {
		mu.Lock()
		answering = v
		mu.Unlock()
	}
	answerWith(vs["hello"])
	randA.queue(vs["hello"].Nonce)
	if _, err := a.eng.Check(ctx, onA.ID); err != nil {
		t.Fatal(err)
	}
	recA := peerRecord(t, storeA, onA.ID)
	if recA.State != msfp.Ours || !reflect.DeepEqual(*recA.Versions, wire.Versions{Core: []int{1}, Parts: map[string][]int{"catalog": {1}}}) ||
		!reflect.DeepEqual(recA.Pins, []string{"E7UUIjYbkigLNKPjmSGixyA-pslmiFKckCEX_A6IZlM"}) {
		t.Errorf("B on A: %+v", recA)
	}
	answerWith(vs["refused hello"])
	randA.queue(vs["refused hello"].Nonce)
	if _, err := a.eng.Check(ctx, onA.ID); !errors.Is(err, msfp.ErrUnknown) {
		t.Fatalf("refused hello: %v", err)
	}
	if recA := peerRecord(t, storeA, onA.ID); recA.State != msfp.Gone || len(recA.Pins) != 1 {
		t.Errorf("B on A: %+v", recA)
	}
}

// serveVector sends a vector's request to an engine's handler.
func serveVector(t *testing.T, e *msfp.Engine, v vector) *httptest.ResponseRecorder {
	t.Helper()
	signer := map[string]string{"a": "A6EHv_POEL4dcN0Y50vAmWfk1jCbpQ1fHdyGZBJVMbg", "b": "Kay64UG8yvCyLhqU000LxzYeUm0L_hLIl5S8kyKWbdc"}[v.Signer]
	r := httptest.NewRequest(v.Method, "https://"+v.Host+v.Target, bytes.NewReader([]byte(v.Body)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("MSFP-Key", signer)
	r.Header.Set("MSFP-Date", v.Date)
	r.Header.Set("MSFP-Nonce", v.NonceText)
	r.Header.Set("MSFP-Signature", v.Signature)
	w := httptest.NewRecorder()
	e.FederationHandler().ServeHTTP(w, r)
	return w
}

func mustKey(t *testing.T, s string) []byte {
	t.Helper()
	k, ok := wire.Decode64(s, 32)
	if !ok {
		t.Fatal("not a key", s)
	}
	return k
}

// peerRecord reads a peer's record straight from a store.
func peerRecord(t *testing.T, st msfp.Store, id string) msfp.PeerRecord {
	t.Helper()
	var p *msfp.PeerRecord
	err := st.Read(context.Background(), func(tx msfp.Tx) error {
		var err error
		p, err = tx.Peer(id)
		return err
	})
	if err != nil || p == nil {
		t.Fatal("no peer", id, err)
	}
	return *p
}

// keyVector is the exchange of rotation.json or revocation.json.
func keyVector(t *testing.T, file string) vector {
	t.Helper()
	var f struct{ Exchange vector }
	b, err := os.ReadFile("wire/testdata/v1/" + file)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f.Exchange
}

// The vectors of key changes (MSFP core sections 17.2 and 17.3) through
// two engines: A's rotate to B and B's answer, then A's revoke, each
// byte for byte; and the pulled exchange, A answering B, a server that
// can't be called, with its rotation and revocation, then under a2.
func TestKeyVectorsThroughEngines(t *testing.T) {
	ctx := context.Background()

	// rotation.json: A rotates from a to a2, committing to a3.
	v := newVectorPair(t)
	want := keyVector(t, "rotation.json")
	v.setClocks(1800100000)
	v.pins(t)
	v.randA.queue(v.keys["a3"].Seed, hex.EncodeToString(mustNonce(t, want.NonceText)))
	if err := v.a.eng.Rotate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	got := v.b.exchanges("rotate")
	if len(got) != 1 || got[0].reqBody != want.Body || got[0].reqSig != want.Signature ||
		got[0].status != want.Status || got[0].resBody != want.AnswerBody || got[0].resSig != want.AnswerSignature {
		t.Errorf("rotate:\n%+v\nwant\n%s %s %d %s %s", got, want.Body, want.Signature, want.Status, want.AnswerBody, want.AnswerSignature)
	}
	if p := peerRecord(t, v.storeB, v.onB.ID); !bytes.Equal(p.Key, mustKey(t, v.keys["a2"].Public)) {
		t.Errorf("A on B: %+v", p)
	}
	if sv, _ := v.a.eng.Server(ctx); !sv.RotationSince.IsZero() || sv.Fingerprint != "KGOK-5E7J-HLIB-DV4F-GVMJ-IQC6" {
		t.Errorf("A: %+v", sv)
	}

	// revocation.json: A revokes a, then rotates to a2 in the same action.
	v = newVectorPair(t)
	want = keyVector(t, "revocation.json")
	v.setClocks(1800200000)
	v.pins(t)
	v.randA.queue(hex.EncodeToString(mustNonce(t, want.NonceText)), v.keys["a3"].Seed)
	if rotated, err := v.a.eng.KeyStolen(ctx, "", false); err != nil || !rotated {
		t.Fatal(rotated, err)
	}
	got = v.b.exchanges("revoke")
	if len(got) != 1 || got[0].reqBody != want.Body || got[0].reqSig != want.Signature ||
		got[0].status != want.Status || got[0].resBody != want.AnswerBody || got[0].resSig != want.AnswerSignature {
		t.Errorf("revoke:\n%+v\nwant\n%s %s %d %s %s", got, want.Body, want.Signature, want.Status, want.AnswerBody, want.AnswerSignature)
	}
	if p := peerRecord(t, v.storeB, v.onB.ID); !bytes.Equal(p.Key, mustKey(t, v.keys["a2"].Public)) || !p.Suspended.IsZero() {
		t.Errorf("A on B: %+v", p)
	}

	// exchanges.json, the last three: B can't be called. A revokes a and
	// rotates to a2: its answer to B's hello, signed with a, carries both
	// statements, newly dated; B's next hello knows a2, and is answered
	// under it.
	v = newVectorPair(t)
	v.clockA.Set(1800000240)
	if res := serveVector(t, v.a.eng, v.vs["hello, from a server that can't be called"]); res.Code != 200 {
		t.Fatalf("B's first hello: %d %s", res.Code, res.Body)
	}
	if p := peerRecord(t, v.storeA, v.onA.ID); p.Inbound != msfp.InboundNo {
		t.Fatalf("B on A: %+v", p)
	}
	v.clockA.Set(1800300000)
	v.randA.queue(v.keys["a3"].Seed)
	if rotated, err := v.a.eng.KeyStolen(ctx, "", false); err != nil || !rotated {
		t.Fatal(rotated, err)
	}
	pulled := v.vs["hello, answered with a rotation and a revocation"]
	res := serveVector(t, v.a.eng, pulled)
	if res.Code != 200 || wire.CheckResponse(mustKey(t, v.keys["a"].Public), pulled.NonceText, 200, res.Header().Get("Content-Type"),
		res.Body.Bytes(), res.Header().Get("MSFP-Signature")) != nil {
		t.Fatalf("A's answer: %d %s", res.Code, res.Body)
	}
	var gotM, wantM struct{ Rotation, Revocation json.RawMessage }
	json.Unmarshal(res.Body.Bytes(), &gotM)
	json.Unmarshal([]byte(pulled.AnswerBody), &wantM)
	if string(gotM.Rotation) != string(wantM.Rotation) || string(gotM.Revocation) != string(wantM.Revocation) {
		t.Errorf("A's statements:\n%s\n%s\nwant\n%s\n%s", gotM.Rotation, gotM.Revocation, wantM.Rotation, wantM.Revocation)
	}
	v.clockA.Set(1800300060)
	known := v.vs["hello, with the new key known"]
	res = serveVector(t, v.a.eng, known)
	if res.Code != 200 || wire.CheckResponse(mustKey(t, v.keys["a2"].Public), known.NonceText, 200, res.Header().Get("Content-Type"),
		res.Body.Bytes(), res.Header().Get("MSFP-Signature")) != nil {
		t.Errorf("A's answer under a2: %d %s", res.Code, res.Body)
	}
	if bytes.Contains(res.Body.Bytes(), []byte(`"rotation"`)) {
		t.Errorf("a rotation still pulled: %s", res.Body)
	}
	if sv, _ := v.a.eng.Server(ctx); !sv.RotationSince.IsZero() {
		t.Errorf("A's change not over: %+v", sv)
	}
}

// mustNonce is a vector's nonce text as its 16 bytes.
func mustNonce(t *testing.T, text string) []byte {
	t.Helper()
	b, ok := wire.Decode64(text, 16)
	if !ok {
		t.Fatal("not a nonce", text)
	}
	return b
}
