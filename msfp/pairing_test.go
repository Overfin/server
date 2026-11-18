package msfp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/overfin/server/msfp"
)

// Two engines pair over TLS: A makes a code, B reads and redeems it (A is
// theirs on B, B ours on A, and A's operator is told), A accepts (both
// paired, by A's hello), and both know the other's versions.
func TestPairing(t *testing.T) {
	ctx := context.Background()
	a := newServer(t, msfp.Config{Name: func() string { return "Server A" }})
	b := newServer(t, msfp.Config{Name: func() string { return "  Server\tB\u200b " }})

	code, view, err := a.eng.MakeCode(ctx, "for B")
	if err != nil {
		t.Fatal(err)
	}
	if view.Note != "for B" || !view.Expires.Equal(view.Created.Add(24*time.Hour)) {
		t.Errorf("code view: %+v", view)
	}
	if codes, _ := a.eng.Codes(ctx); len(codes) != 1 || codes[0].ID != view.ID {
		t.Errorf("codes: %+v", codes)
	}
	aServer, _ := a.eng.Server(ctx)
	preview, err := b.eng.ReadCode(ctx, code)
	if err != nil || preview.Fingerprint != aServer.Fingerprint || preview.Address != a.srv.URL {
		t.Fatalf("preview %+v, %v; A is %s", preview, err, aServer.Fingerprint)
	}

	theirs, err := b.eng.RedeemCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if theirs.State != msfp.Theirs || theirs.Name != "Server A" || theirs.Fingerprint != aServer.Fingerprint {
		t.Errorf("on B: %+v", theirs)
	}
	ours := onlyPeer(t, a.eng)
	bServer, _ := b.eng.Server(ctx)
	if ours.State != msfp.Ours || ours.Name != "Server B" || ours.Fingerprint != bServer.Fingerprint {
		t.Errorf("on A: %+v", ours)
	}
	if codes, _ := a.eng.Codes(ctx); len(codes) != 0 {
		t.Error("the code is used up")
	}
	if evs := a.events.all(); len(evs) != 1 || evs[0].(msfp.PeerWaiting).Fingerprint != bServer.Fingerprint {
		t.Errorf("A's events: %#v", evs)
	}

	paired, err := a.eng.Accept(ctx, ours.ID)
	if err != nil {
		t.Fatal(err)
	}
	if paired.State != msfp.Paired || paired.Core != 1 {
		t.Errorf("on A: %+v", paired)
	}
	if p := onlyPeer(t, b.eng); p.State != msfp.Paired || p.Core != 1 || p.Paired.IsZero() {
		t.Errorf("on B: %+v", p)
	}
	if _, err := a.eng.Accept(ctx, ours.ID); !errors.Is(err, msfp.ErrNotWaiting) {
		t.Errorf("accepted twice: %v", err)
	}
	if !hasEvent[msfp.PeerPaired](b.events.all()) || !hasEvent[msfp.VersionsChanged](b.events.all()) {
		t.Errorf("B's events: %#v", b.events.all())
	}
}

// Ending: the ender forgets every key of the peer and tells it; the
// other deletes the ender. A new code pairs them again.
func TestEnd(t *testing.T) {
	ctx := context.Background()
	a := newServer(t, msfp.Config{})
	b := newServer(t, msfp.Config{})
	onA, _ := pair(t, a, b)
	if err := a.eng.End(ctx, onA.ID); err != nil {
		t.Fatal(err)
	}
	if peers, _ := b.eng.Peers(ctx); len(peers) != 0 {
		t.Errorf("B still has %+v", peers)
	}
	if !hasEvent[msfp.PeerEnded](b.events.all()) {
		t.Error("B wasn't told")
	}
	if err := a.eng.End(ctx, onA.ID); !errors.Is(err, msfp.ErrNotFound) {
		t.Errorf("ended twice: %v", err)
	}
	// Redeeming again is consent to pair again.
	onA, onB := pair(t, a, b)
	if onA.State != msfp.Paired || onB.State != msfp.Paired {
		t.Errorf("again: %+v, %+v", onA, onB)
	}
}

// A peer that missed the end learns by its next hello: the signed 404 of
// a forgotten key, and it marks the ender gone. A gone peer that was
// paired comes back by a paired hello only through a new pairing here:
// the ender forgot it.
func TestGone(t *testing.T) {
	ctx := context.Background()
	a := newServer(t, msfp.Config{})
	b := newServer(t, msfp.Config{})
	onA, onB := pair(t, a, b)
	b.setDown(true)
	if err := a.eng.End(ctx, onA.ID); err != nil {
		t.Fatal(err)
	}
	b.setDown(false)
	if _, err := b.eng.Check(ctx, onB.ID); !errors.Is(err, msfp.ErrUnknown) {
		t.Fatalf("check: %v", err)
	}
	if p := onlyPeer(t, b.eng); p.State != msfp.Gone {
		t.Errorf("on B: %+v", p)
	}
	if !hasEvent[msfp.PeerGone](b.events.all()) {
		t.Error("no PeerGone")
	}
	// Gone, it stays a peer of B's, so A's new code is refused at once,
	// until B's operator removes it (MSFP core section 7.1, step 2).
	code, _, _ := a.eng.MakeCode(ctx, "")
	if _, err := b.eng.RedeemCode(ctx, code); !errors.Is(err, msfp.ErrAlreadyPeer) {
		t.Errorf("redeem: %v", err)
	}
	if err := b.eng.End(ctx, onB.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.eng.RedeemCode(ctx, code); err != nil {
		t.Errorf("redeem after removing: %v", err)
	}
}

// The panel open: a peer in state theirs gets a hello, and learns it is
// paired once the other operator accepted while it couldn't be told.
func TestPanelOpen(t *testing.T) {
	ctx := context.Background()
	a := newServer(t, msfp.Config{})
	b := newServer(t, msfp.Config{})
	code, _, _ := a.eng.MakeCode(ctx, "")
	if _, err := b.eng.RedeemCode(ctx, code); err != nil {
		t.Fatal(err)
	}
	b.setDown(true)
	if _, err := a.eng.Accept(ctx, onlyPeer(t, a.eng).ID); err != nil {
		t.Fatal(err)
	}
	b.setDown(false)
	if onlyPeer(t, b.eng).State != msfp.Theirs {
		t.Fatal("B was told while down")
	}
	b.eng.PanelOpen()
	deadline := time.Now().Add(5 * time.Second)
	for onlyPeer(t, b.eng).State != msfp.Paired {
		if time.Now().After(deadline) {
			t.Fatal("B never learnt")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func hasEvent[T msfp.Event](evs []msfp.Event) bool {
	for _, e := range evs {
		if _, ok := e.(T); ok {
			return true
		}
	}
	return false
}
