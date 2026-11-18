package store

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/overfin/server/msfp/wire"
)

// This server's setting: its address made once and kept, on or off; on,
// its codes not used yet withdrawn.
func TestFederationUncallable(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	made := 0
	newAddress := func() string { made++; return wire.NewUncallableAddress() }
	on, first, err := s.FederationUncallable(ctx, newAddress)
	if err != nil || on || wire.CheckUncallable(first) != nil {
		t.Fatalf("at first: %v %q %v", on, first, err)
	}
	if _, err := s.CreatePairingCode(ctx, bytes.Repeat([]byte{1}, 32), "", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFederationUncallable(ctx, true); err != nil {
		t.Fatal(err)
	}
	if on, again, err := s.FederationUncallable(ctx, newAddress); err != nil || !on || again != first || made != 1 {
		t.Fatalf("on: %v %q (first %q), made %d, %v", on, again, first, made, err)
	}
	if codes, _ := s.PairingCodes(ctx); len(codes) != 0 {
		t.Errorf("codes left: %+v", codes)
	}
	s.SetFederationUncallable(ctx, false)
	if on, again, _ := s.FederationUncallable(ctx, newAddress); on || again != first {
		t.Errorf("off: %v %q", on, again)
	}
}

// A peer that can't be called: what it held for us dropped, its address
// stored as it says, a move to confirm dropped; it starts as one that can
// be called, but a redeemer that said it can't.
func TestPeerInbound(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	p, err := s.AddPeer(ctx, bytes.Repeat([]byte{3}, 32), "https://b.example.org", "B", nil)
	if err != nil || p.Inbound != InboundYes || !p.Callable() {
		t.Fatalf("added: %+v, %v", p, err)
	}
	s.PutPeerTitles(ctx, p.ID, PeerReading(), []PeerTitle{{Key: "movie/tmdb/1", Kind: "movie", Library: "x"}})
	s.SetPeerMove(ctx, p.ID, &wire.Move{Address: "https://c.example.org"})
	if err := s.SetPeerInbound(ctx, p.ID, InboundNo); err != nil {
		t.Fatal(err)
	}
	q, _ := s.PeerByID(ctx, p.ID)
	if q.Inbound != InboundNo || q.Callable() || q.Move != nil {
		t.Errorf("can't be called: %+v", q)
	}
	if n, _ := s.PeerTitleCounts(ctx); n[p.ID] != 0 {
		t.Errorf("its titles kept: %v", n)
	}
	invalid := wire.NewUncallableAddress()
	s.SetPeerAddress(ctx, p.ID, invalid)
	s.SetPeerCallsFailed(ctx, p.ID, true)
	s.SetPeerInbound(ctx, p.ID, InboundConfirming)
	if q, _ := s.PeerByID(ctx, p.ID); q.Address != invalid || !q.CallsFailed || q.Callable() {
		t.Errorf("confirming: %+v", q)
	}
	if err := s.SetPeerInbound(ctx, p.ID, "maybe"); err == nil {
		t.Error("another value kept")
	}

	secret := bytes.Repeat([]byte{8}, 32)
	s.CreatePairingCode(ctx, secret, "", time.Now().Add(time.Hour))
	r, err := s.RedeemPairingCode(ctx, PairingSecretHash(secret), bytes.Repeat([]byte{4}, 32), invalid, "P", nil, true,
		func([]byte) bool { return true })
	if err != nil || r.Inbound != InboundNo {
		t.Fatalf("redeemed: %+v, %v", r, err)
	}
	if q, _ := s.PeerByID(ctx, r.ID); q.Callable() {
		t.Errorf("stored: %+v", q)
	}
}
