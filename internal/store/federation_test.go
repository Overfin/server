package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

// The key is made once: a second start racing the first keeps the first.
func TestFederationSeed(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	if _, ok, err := s.FederationSeed(ctx); ok || err != nil {
		t.Fatalf("a key before any was made: %v %v", ok, err)
	}
	first := bytes.Repeat([]byte{1}, 32)
	if kept, err := s.KeepFederationSeed(ctx, first); err != nil || !bytes.Equal(kept, first) {
		t.Fatalf("kept %x, %v", kept, err)
	}
	if kept, err := s.KeepFederationSeed(ctx, bytes.Repeat([]byte{2}, 32)); err != nil || !bytes.Equal(kept, first) {
		t.Fatalf("a second key replaced the first: %x, %v", kept, err)
	}
}

// A code is redeemed once, by its secret, only if the check passes and
// before it expires; the server that used it waits for our operator.
func TestRedeemPairingCode(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	clock := time.Unix(1_800_000_000, 0)
	s.now = func() time.Time { return clock }
	secret := bytes.Repeat([]byte{7}, 32)
	_, err := s.CreatePairingCode(ctx, secret, "for Marc", clock.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if list, _ := s.PairingCodes(ctx); len(list) != 1 || list[0].Note != "for Marc" {
		t.Fatalf("codes: %+v", list)
	}
	key := bytes.Repeat([]byte{3}, 32)
	hash := PairingSecretHash(secret)
	refuse := func([]byte) bool { return false }
	if _, err := s.RedeemPairingCode(ctx, hash, key, "https://b.example.org", "B", refuse); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refused proof: %v", err)
	}
	if _, err := s.RedeemPairingCode(ctx, PairingSecretHash([]byte("other")), key, "https://b.example.org", "B",
		func([]byte) bool { return true }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another secret: %v", err)
	}
	var seen []byte
	p, err := s.RedeemPairingCode(ctx, hash, key, "https://b.example.org", "B", func(got []byte) bool { seen = got; return true })
	if err != nil || p.State != PeerOurs || !bytes.Equal(seen, secret) {
		t.Fatalf("redeemed: %+v, %v (secret %x)", p, err, seen)
	}
	if _, err := s.RedeemPairingCode(ctx, hash, bytes.Repeat([]byte{4}, 32), "https://c.example.org", "C",
		func([]byte) bool { return true }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a code used twice: %v", err)
	}
	if list, _ := s.PairingCodes(ctx); len(list) != 0 {
		t.Fatalf("a used code is still listed: %+v", list)
	}

	// An expired code.
	secret2 := bytes.Repeat([]byte{8}, 32)
	if _, err := s.CreatePairingCode(ctx, secret2, "", clock.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(2 * time.Hour)
	if _, err := s.RedeemPairingCode(ctx, PairingSecretHash(secret2), bytes.Repeat([]byte{5}, 32), "https://d.example.org", "D",
		func([]byte) bool { return true }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an expired code: %v", err)
	}

	// A key already known keeps the code.
	secret3 := bytes.Repeat([]byte{6}, 32)
	if _, err := s.CreatePairingCode(ctx, secret3, "", clock.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemPairingCode(ctx, PairingSecretHash(secret3), key, "https://b.example.org", "B",
		func([]byte) bool { return true }); !errors.Is(err, ErrPeerExists) {
		t.Fatalf("a known key: %v", err)
	}
	if list, _ := s.PairingCodes(ctx); len(list) != 1 {
		t.Fatalf("the code was used up by a known key: %+v", list)
	}
}

func TestPeers(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	key := bytes.Repeat([]byte{3}, 32)
	p, err := s.AddPeer(ctx, key, "https://a.example.org", "A")
	if err != nil || p.State != PeerTheirs {
		t.Fatalf("added: %+v, %v", p, err)
	}
	if _, err := s.AddPeer(ctx, key, "https://a.example.org", "A"); !errors.Is(err, ErrPeerExists) {
		t.Fatalf("added twice: %v", err)
	}
	if ok, _ := s.SetPeerState(ctx, p.ID, PeerOurs, PeerPaired); ok {
		t.Fatal("moved from a state it wasn't in")
	}
	if ok, err := s.SetPeerState(ctx, p.ID, PeerTheirs, PeerPaired); !ok || err != nil {
		t.Fatalf("pairing: %v %v", ok, err)
	}
	got, err := s.PeerByKey(ctx, key)
	if err != nil || got.State != PeerPaired || got.Paired == nil {
		t.Fatalf("by key: %+v, %v", got, err)
	}
	if list, _ := s.Peers(ctx); len(list) != 1 {
		t.Fatalf("peers: %+v", list)
	}
	if err := s.DeletePeer(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PeerByID(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("still there: %v", err)
	}
	if err := s.DeletePeer(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted twice: %v", err)
	}
}

// What we offer and want is per peer, replaced whole, and goes with it.
func TestPeerSharing(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	p, _ := s.AddPeer(ctx, bytes.Repeat([]byte{3}, 32), "https://a.example.org", "A")
	if err := s.SetPeerOffers(ctx, p.ID, []string{"lib2", "lib1", "lib1"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.PeerOffers(ctx, p.ID); len(got) != 2 || got[0] != "lib1" {
		t.Fatalf("offers: %v", got)
	}
	s.SetPeerOffers(ctx, p.ID, []string{"lib3"})
	if got, _ := s.PeerOffers(ctx, p.ID); len(got) != 1 || got[0] != "lib3" {
		t.Fatalf("offers replaced: %v", got)
	}
	if err := s.SetPeerWants(ctx, p.ID, []PeerLibrary{{ID: "x", Name: "Films", Kind: "movies"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.PeerWants(ctx, p.ID); len(got) != 1 || got[0].Name != "Films" {
		t.Fatalf("wants: %v", got)
	}
	s.DeletePeer(ctx, p.ID)
	if got, _ := s.PeerOffers(ctx, p.ID); len(got) != 0 {
		t.Fatalf("offers outlived the peer: %v", got)
	}
	if got, _ := s.PeerWants(ctx, p.ID); len(got) != 0 {
		t.Fatalf("wants outlived the peer: %v", got)
	}
}
