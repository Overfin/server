package store

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/overfin/server/internal/msfp"
)

// keyedTest is a store with a federation key, k.
func keyedTest(t *testing.T) (*Store, msfp.Key) {
	t.Helper()
	s, k := openTest(t), msfp.NewKey()
	if _, err := s.KeepFederationSeed(context.Background(), k.Seed()); err != nil {
		t.Fatal(err)
	}
	return s, k
}

// A change of key ends only as the change it was started for: ended with
// another old key, or moved to another key than the current one, nothing
// changes.
func TestFederationRotationEndsItsOwn(t *testing.T) {
	ctx := context.Background()
	s, first := keyedTest(t)
	p, err := s.AddPeer(ctx, msfp.NewKey().Public(), "https://b.example", "B", nil)
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.AddPeer(ctx, msfp.NewKey().Public(), "https://c.example", "C", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, after := msfp.NewKey(), msfp.NewKey()
	if err := s.RotateFederationKey(ctx, second.Seed(), after.Seed(), msfp.Commitment(after.Public()), time.Now(), false); err != nil {
		t.Fatal(err)
	}
	other := msfp.NewKey().Public()
	if ended, _, err := s.FinishFederationRotation(ctx, other); err != nil || ended {
		t.Fatalf("ended with another old key: %v, %v", ended, err)
	}
	if moved, _, err := s.PeerMovedTo(ctx, p.ID, first.Public()); err != nil || moved {
		t.Fatalf("moved to the old key: %v, %v", moved, err)
	}
	if k, err := s.FederationKeys(ctx); err != nil || k.Old == nil {
		t.Fatalf("the old key: %v", err)
	}
	if got, _ := s.PeerByID(ctx, p.ID); got.OurKey != OurKeyOld {
		t.Fatalf("B: %q", got.OurKey)
	}
	// B moves, then C, the last: the change ends with it.
	if moved, ended, err := s.PeerMovedTo(ctx, p.ID, second.Public()); err != nil || !moved || ended {
		t.Fatalf("B's move: %v %v %v", moved, ended, err)
	}
	if moved, ended, err := s.PeerMovedTo(ctx, q.ID, second.Public()); err != nil || !moved || !ended {
		t.Fatalf("C's move: %v %v %v", moved, ended, err)
	}
	if k, err := s.FederationKeys(ctx); err != nil || k.Old != nil {
		t.Fatalf("the old key after every peer moved: %v", err)
	}
	// Ended late, it changes nothing either.
	if ended, _, err := s.FinishFederationRotation(ctx, first.Public()); err != nil || ended {
		t.Fatalf("ended twice: %v, %v", ended, err)
	}
}

// Replacing the key ends every pairing and replaces the key together, or
// neither: a failure in between (here, the key's write refused) leaves
// both as they were.
func TestReplaceFederationKeyAllOrNothing(t *testing.T) {
	ctx := context.Background()
	s, k := keyedTest(t)
	peerKey := msfp.NewKey().Public()
	if _, err := s.AddPeer(ctx, peerKey, "https://b.example", "B", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPeer(ctx, msfp.NewKey().Public(), "https://c.example", "C", nil); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TRIGGER no_key_insert BEFORE INSERT ON meta WHEN NEW.name = 'federation-key'
			BEGIN SELECT RAISE(ABORT, 'refused for the test'); END`,
		`CREATE TRIGGER no_key_update BEFORE UPDATE ON meta WHEN NEW.name = 'federation-key'
			BEGIN SELECT RAISE(ABORT, 'refused for the test'); END`,
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	fresh, after := msfp.NewKey(), msfp.NewKey()
	if err := s.ReplaceFederationKey(ctx, fresh.Seed(), after.Seed(), msfp.Commitment(after.Public())); err == nil {
		t.Fatal("replaced, its write refused")
	}
	if peers, err := s.Peers(ctx); err != nil || len(peers) != 2 {
		t.Fatalf("pairings after a failed replace: %d, %v", len(peers), err)
	}
	if f, _ := s.PeerForgotten(ctx, peerKey); f {
		t.Fatal("a peer forgotten by a failed replace")
	}
	if seed, _, err := s.FederationSeed(ctx); err != nil || !bytes.Equal(seed, k.Seed()) {
		t.Fatalf("the key after a failed replace: %v", err)
	}
	// Allowed, it does both.
	for _, q := range []string{`DROP TRIGGER no_key_insert`, `DROP TRIGGER no_key_update`} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ReplaceFederationKey(ctx, fresh.Seed(), after.Seed(), msfp.Commitment(after.Public())); err != nil {
		t.Fatal(err)
	}
	if peers, err := s.Peers(ctx); err != nil || len(peers) != 0 {
		t.Fatalf("pairings after the replace: %d, %v", len(peers), err)
	}
	if f, _ := s.PeerForgotten(ctx, peerKey); !f {
		t.Fatal("a peer's key not forgotten")
	}
	if seed, _, err := s.FederationSeed(ctx); err != nil || !bytes.Equal(seed, fresh.Seed()) {
		t.Fatalf("the key after the replace: %v", err)
	}
}

// Of contacts at once with a peer on our old key, one only gets to send
// it a rotate; another may once the spacing passed, and none to a peer
// that moved.
func TestClaimRotate(t *testing.T) {
	ctx := context.Background()
	s, _ := keyedTest(t)
	p, err := s.AddPeer(ctx, msfp.NewKey().Public(), "https://b.example", "B", nil)
	if err != nil {
		t.Fatal(err)
	}
	next, after := msfp.NewKey(), msfp.NewKey()
	if err := s.RotateFederationKey(ctx, next.Seed(), after.Seed(), msfp.Commitment(after.Public()), time.Now(), false); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	var claimed atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.ClaimRotate(ctx, p.ID, now, now.Add(-time.Minute))
			if err != nil {
				t.Error(err)
			}
			if ok {
				claimed.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := claimed.Load(); n != 1 {
		t.Fatalf("%d contacts at once sent a rotate", n)
	}
	later := now.Add(59 * time.Second)
	if ok, err := s.ClaimRotate(ctx, p.ID, later, later.Add(-time.Minute)); err != nil || ok {
		t.Fatalf("within the spacing: %v, %v", ok, err)
	}
	later = now.Add(time.Minute)
	if ok, err := s.ClaimRotate(ctx, p.ID, later, later.Add(-time.Minute)); err != nil || !ok {
		t.Fatalf("after the spacing: %v, %v", ok, err)
	}
	if _, _, err := s.PeerMovedTo(ctx, p.ID, next.Public()); err != nil {
		t.Fatal(err)
	}
	later = later.Add(time.Hour)
	if ok, err := s.ClaimRotate(ctx, p.ID, later, later.Add(-time.Minute)); err != nil || ok {
		t.Fatalf("to a peer that moved: %v, %v", ok, err)
	}
}
