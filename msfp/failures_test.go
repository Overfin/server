package msfp_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/msfp"
	"github.com/overfin/server/msfp/store/memory"
	"github.com/overfin/server/msfp/wire"
)

// Codes that aren't, or no longer are, good.
func TestBadCodes(t *testing.T) {
	ctx := context.Background()
	clockA, clockB := newClock(1800000000), newClock(1800000000)
	a := newServer(t, msfp.Config{Clock: clockA})
	b := newServer(t, msfp.Config{Clock: clockB})
	code, view, err := a.eng.MakeCode(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := wire.ParseCode(code)

	for _, text := range []string{"", "hello", strings.ToUpper(code), code[:40], code + "!"} {
		if _, err := b.eng.RedeemCode(ctx, text); !errors.Is(err, msfp.ErrCodeInvalid) {
			t.Errorf("%q: %v", text, err)
		}
	}
	// Its own code.
	if _, err := a.eng.RedeemCode(ctx, code); !errors.Is(err, msfp.ErrOwnCode) {
		t.Errorf("own code: %v", err)
	}
	// Another secret: the proof fails, A refuses, unsigned.
	other := c
	other.Secret = bytes.Repeat([]byte{7}, 32)
	if _, err := b.eng.RedeemCode(ctx, other.String()); !errors.Is(err, msfp.ErrCodeRefused) {
		t.Errorf("wrong secret: %v", err)
	}
	// Another key than the maker's: its answer doesn't verify, nothing is
	// kept on B.
	wrongKey := c
	wrongKey.Key = wire.NewKey().Public()
	if _, err := b.eng.RedeemCode(ctx, wrongKey.String()); !errors.Is(err, msfp.ErrUnreachable) {
		t.Errorf("wrong key: %v", err)
	}
	if peers, _ := b.eng.Peers(ctx); len(peers) != 0 {
		t.Errorf("kept %+v", peers)
	}
	// That redemption used the code up on A (A had no way to know).
	// Expired on the redeemer's clock: refused before any call.
	code, _, _ = a.eng.MakeCode(ctx, "")
	clockB.Add(25 * time.Hour)
	if _, err := b.eng.RedeemCode(ctx, code); !errors.Is(err, msfp.ErrCodeExpired) {
		t.Errorf("expired here: %v", err)
	}
	// Expired on the maker's: A refuses it.
	clockB.Set(1800000000)
	clockA.Add(25 * time.Hour)
	if _, err := b.eng.RedeemCode(ctx, code); !errors.Is(err, msfp.ErrCodeRefused) {
		t.Errorf("expired there: %v", err)
	}
	// Withdrawn.
	clockA.Set(1800000000)
	if err := a.eng.DeleteCode(ctx, view.ID); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("delete a used code: %v", err)
	}
	code, view, _ = a.eng.MakeCode(ctx, "")
	if err := a.eng.DeleteCode(ctx, view.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.eng.RedeemCode(ctx, code); !errors.Is(err, msfp.ErrCodeRefused) {
		t.Errorf("withdrawn: %v", err)
	}
	// RunOnce drops expired codes.
	a.eng.MakeCode(ctx, "")
	clockA.Add(25 * time.Hour)
	if err := a.eng.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	clockA.Set(1800000000)
	if codes, _ := a.eng.Codes(ctx); len(codes) != 0 {
		t.Errorf("expired codes kept: %+v", codes)
	}
}

// A code redeemed by a server that is already a peer: refused at once
// on its side; when it forgot the maker, the maker answers 409, and the
// code stays live.
func TestAlreadyPeer(t *testing.T) {
	ctx := context.Background()
	storeB := memory.New()
	a := newServer(t, msfp.Config{})
	b := newServer(t, msfp.Config{Store: storeB})
	_, onB := pair(t, a, b)
	code, _, _ := a.eng.MakeCode(ctx, "")
	if _, err := b.eng.RedeemCode(ctx, code); !errors.Is(err, msfp.ErrAlreadyPeer) {
		t.Errorf("here: %v", err)
	}
	storeB.Update(ctx, func(tx msfp.Tx) error { return tx.DeletePeer(onB.ID) })
	if _, err := b.eng.RedeemCode(ctx, code); !errors.Is(err, msfp.ErrAlreadyPeer) {
		t.Errorf("there: %v", err)
	}
	if got := a.lastExchange(); got.status != http.StatusConflict || got.resBody != "" || got.resSig == "" {
		t.Errorf("the 409: %+v", got)
	}
	if codes, _ := a.eng.Codes(ctx); len(codes) != 1 {
		t.Error("the code was used up")
	}
}

// A request sent twice is refused the second time, as a stranger's.
func TestReplay(t *testing.T) {
	ctx := context.Background()
	a := newServer(t, msfp.Config{})
	b := newServer(t, msfp.Config{})
	_, onB := pair(t, a, b)
	var raw *http.Request
	var body []byte
	a.onRequest(func(r *http.Request, b []byte) { raw, body = r, b })
	if _, err := b.eng.Check(ctx, onB.ID); err != nil {
		t.Fatal(err)
	}
	again := httptest.NewRequest(http.MethodPost, raw.URL.String(), bytes.NewReader(body))
	again.Header = raw.Header.Clone()
	w := httptest.NewRecorder()
	a.eng.FederationHandler().ServeHTTP(w, again)
	if w.Code != http.StatusNotFound || w.Header().Get("MSFP-Signature") != "" {
		t.Errorf("replayed: %d %s", w.Code, w.Header())
	}
}

// Past its own limit, a peer gets a signed 429 with retryAfter, and this
// server's calls to it hold until then; past the limit by address, a
// stranger gets the plain 404.
func TestRateLimits(t *testing.T) {
	ctx := context.Background()
	a := newServer(t, msfp.Config{Limits: msfp.CallLimits{PeerRate: 0.001, PeerBurst: 2}})
	b := newServer(t, msfp.Config{})
	_, onB := pair(t, a, b) // A's hello to B, then B's none: A's accept hello was A's call
	if _, err := b.eng.Check(ctx, onB.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.eng.Check(ctx, onB.ID); err != nil {
		t.Fatal(err)
	}
	_, err := b.eng.Check(ctx, onB.ID)
	if !errors.Is(err, msfp.ErrRateLimited) {
		t.Fatalf("third: %v", err)
	}
	if got := a.lastExchange(); got.status != http.StatusTooManyRequests || got.resSig == "" || !strings.Contains(got.resBody, `"retryAfter":`) {
		t.Errorf("the 429: %+v", got)
	}
	if _, err := b.eng.Check(ctx, onB.ID); !errors.Is(err, msfp.ErrBackingOff) {
		t.Errorf("fourth: %v", err)
	}

	// By address, before any signature: the pair passes, then even a
	// peer's signed hello gets the plain 404.
	c := newServer(t, msfp.Config{Limits: msfp.CallLimits{AddressRate: 0.001, AddressBurst: 1}})
	d := newServer(t, msfp.Config{})
	_, onD := pair(t, c, d)
	if _, err := d.eng.Check(ctx, onD.ID); !errors.Is(err, msfp.ErrUnreachable) {
		t.Errorf("by address: %v", err)
	}
	if got := c.lastExchange(); got.status != http.StatusNotFound || got.resSig != "" {
		t.Errorf("by address: %+v", got)
	}
}

// Paths the engine doesn't serve, and methods other than POST, get the
// host's not-found; so do strangers.
func TestNotFound(t *testing.T) {
	hostNotFound := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, "host's 404")
	})
	a := newServer(t, msfp.Config{NotFound: hostNotFound})
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/federation/hello", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/federation/nothing", strings.NewReader("{}")),
		httptest.NewRequest(http.MethodPost, "/api/v1/federation/hello", strings.NewReader("{}")),
		httptest.NewRequest(http.MethodPost, "/api/v1/federation/pair", strings.NewReader("{}")),
	} {
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		a.eng.FederationHandler().ServeHTTP(w, req)
		if w.Code != 404 || w.Body.String() != "host's 404" {
			t.Errorf("%s %s: %d %q", req.Method, req.URL.Path, w.Code, w.Body)
		}
	}
}
