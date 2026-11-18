package msfp_test

import (
	"bytes"
	"context"
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

func TestVectorsThroughEngines(t *testing.T) {
	ctx := context.Background()
	vs, keys, codeText := readVectors(t)
	clockA, clockB := newClock(1800000000), newClock(1800000000)
	randA, randB := &script{}, &script{}
	randA.queue(keys["a"].Seed, keys["a2"].Seed, ids, "404142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f")
	randB.queue(keys["b"].Seed, keys["b2"].Seed, ids, vs["pair"].Nonce)
	dialA, dialB := &routeDialer{}, &routeDialer{}
	storeA, storeB := memory.New(), memory.New()
	a := newServer(t, msfp.Config{Address: "https://a.example.org", Name: func() string { return "Server A" },
		Store: storeA, Clock: clockA, Random: randA, Dialer: dialA})
	b := newServer(t, msfp.Config{Address: "https://192.0.2.10:8096", Name: func() string { return "Server B" },
		Store: storeB, Clock: clockB, Random: randB, Dialer: dialB})

	// pair, B to A: the code A makes is the vector's; B's request and A's
	// answer are the vector's, byte for byte.
	code, _, err := a.eng.MakeCode(ctx, "")
	if err != nil || code != codeText {
		t.Fatalf("A's code %q, %v", code, err)
	}
	dialB.set(a.srv.Listener.Addr().String())
	onB, err := b.eng.RedeemCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	got, want := a.lastExchange(), vs["pair"]
	if got.reqBody != want.Body || got.reqSig != want.Signature {
		t.Errorf("B's pair request:\n%s %s\nwant\n%s %s", got.reqBody, got.reqSig, want.Body, want.Signature)
	}
	if got.status != 200 || got.resBody != want.AnswerBody || got.resSig != want.AnswerSignature {
		t.Errorf("A's pair answer:\n%d %s %s\nwant\n%s %s", got.status, got.resBody, got.resSig, want.AnswerBody, want.AnswerSignature)
	}
	if onB.State != msfp.Theirs || onB.Name != "Server A" || onB.Address != "https://a.example.org" {
		t.Errorf("A on B: %+v", onB)
	}
	onA := onlyPeer(t, a.eng)
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
	want = vs["play, to a peer without playback"]
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
