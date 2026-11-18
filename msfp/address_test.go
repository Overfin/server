package msfp_test

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/msfp"
	"github.com/overfin/server/msfp/wire"
)

// A moves from A1 to A2 (MSFP core section 13). It reaches itself at A2
// at once and tells its peers: B confirms A2 by a hello there and moves;
// C can't reach A2, keeps calling A1, which A still accepts, and is
// warned after a day; A points C out after 3 days, and after 30 days
// accepts A2's host only.
func TestMoveConfirmed(t *testing.T) {
	ctx := context.Background()
	clock := newClock(1800000000)
	cDial := &mapDialer{}
	a := newServer(t, msfp.Config{Clock: clock})
	b := newServer(t, msfp.Config{Clock: clock})
	c := newServer(t, msfp.Config{Clock: clock, Dialer: cDial})
	_, bOnB := pair(t, a, b)
	cOnA, cOnC := pair(t, a, c)
	a2 := a.alias(t, true, nil)
	cDial.set(hostOf(a2.srv), closedAddr(t)) // C doesn't reach A2
	a1 := a.srv.URL

	if err := a.eng.SetAddress(ctx, a2.srv.URL); err != nil {
		t.Fatal(err)
	}
	waitAll(a, b, c)
	if _, ok := has[msfp.AddressAnnounced](a.events); !ok {
		t.Fatal("A2 not announced")
	}
	if p, _ := b.eng.Peer(ctx, bOnB.ID); p.Address != a2.srv.URL || p.Moving != nil {
		t.Errorf("A on B: %+v", p)
	}
	if ev, ok := has[msfp.PeerMoved](b.events); !ok || ev.From != a1 || ev.To != a2.srv.URL {
		t.Errorf("B's PeerMoved: %+v", ev)
	}
	p, _ := c.eng.Peer(ctx, cOnC.ID)
	if p.Address != a1 || p.Moving == nil || p.Moving.Address != a2.srv.URL || p.Moving.FailingSince.IsZero() {
		t.Fatalf("A on C: %+v %+v", p, p.Moving)
	}
	sv, _ := a.eng.Server(ctx)
	if sv.Address != a2.srv.URL || sv.Named != a2.srv.URL || sv.Former != a1 || sv.Waiting || sv.PeersMoved != 1 || sv.PeersToMove != 2 {
		t.Errorf("A: %+v", sv)
	}

	// C still calls A1: accepted, and its view says it hasn't moved.
	if _, err := c.eng.Check(ctx, cOnC.ID); err != nil {
		t.Fatal(err)
	}
	if v, _ := a.eng.Peer(ctx, cOnA.ID); !v.NotMoved || v.MoveLate {
		t.Errorf("C on A: %+v", v)
	}

	// C tries again at most hourly; after a day it is warned, once.
	tried := p.Moving.FailingSince
	clock.Add(30 * time.Minute)
	c.eng.RunOnce(ctx)
	if p, _ := c.eng.Peer(ctx, cOnC.ID); p.Moving == nil || p.Moving.Warn {
		t.Errorf("A on C: %+v", p.Moving)
	}
	for range 25 {
		clock.Add(time.Hour)
		c.eng.RunOnce(ctx)
	}
	waitAll(a, b, c)
	if ev, ok := has[msfp.PeerMoveFailing](c.events); !ok || !ev.Since.Equal(tried) || ev.Address != a2.srv.URL {
		t.Errorf("C's warning: %+v", ev)
	}
	if p, _ := c.eng.Peer(ctx, cOnC.ID); p.Address != a1 || p.Moving == nil || !p.Moving.Warn {
		t.Errorf("A on C: %+v", p)
	}

	// 3 days after: A points C out.
	clock.Add(48 * time.Hour)
	a.eng.RunOnce(ctx)
	if _, ok := has[msfp.NotMovedLate](a.events); !ok {
		t.Error("C not pointed out")
	}
	if v, _ := a.eng.Peer(ctx, cOnA.ID); !v.MoveLate {
		t.Errorf("C on A: %+v", v)
	}

	// After 30 days, A1's host is no longer accepted.
	clock.Add(30 * 24 * time.Hour)
	a.eng.RunOnce(ctx)
	if ev, ok := has[msfp.MoveEnded](a.events); !ok || ev.NotMoved != 1 {
		t.Errorf("A's move: %+v %v", ev, ok)
	}
	if sv, _ := a.eng.Server(ctx); sv.Former != "" || !sv.MovedAt.IsZero() {
		t.Errorf("A: %+v", sv)
	}
	if _, err := c.eng.Check(ctx, cOnC.ID); !errors.Is(err, msfp.ErrUnreachable) {
		t.Errorf("C at A1 after 30 days: %v", err)
	}
	if _, err := b.eng.Check(ctx, bOnB.ID); err != nil {
		t.Errorf("B at A2: %v", err)
	}
}

// The last peer to move ends the move at once; a restart keeps it.
func TestMoveEndsWhenAllMoved(t *testing.T) {
	ctx := context.Background()
	clock := newClock(1800000000)
	a := newServer(t, msfp.Config{Clock: clock})
	b := newServer(t, msfp.Config{Clock: clock})
	pair(t, a, b)
	a2 := a.alias(t, true, nil)
	if err := a.eng.SetAddress(ctx, a2.srv.URL); err != nil {
		t.Fatal(err)
	}
	waitAll(a, b)
	if ev, ok := has[msfp.MoveEnded](a.events); !ok || ev.NotMoved != 0 {
		t.Errorf("A's move: %+v %v", ev, ok)
	}
	if sv, _ := a.eng.Server(ctx); sv.Former != "" || sv.Address != a2.srv.URL {
		t.Errorf("A: %+v", sv)
	}
	// The same address again: nothing to do.
	if err := a.eng.SetAddress(ctx, a2.srv.URL); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"https://a.example.org/", "http://a.example.org", "https://msfp-4dq6fy7e4xtop2hj5lv6z3po54.invalid"} {
		if err := a.eng.SetAddress(ctx, bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

// A2 is announced only once A reaches itself there; until then A's
// hellos name A1, and B keeps A1.
func TestAnnounceWaitsForReach(t *testing.T) {
	ctx := context.Background()
	clock := newClock(1800000000)
	a := newServer(t, msfp.Config{Clock: clock})
	b := newServer(t, msfp.Config{Clock: clock})
	_, bOnB := pair(t, a, b)
	a2 := a.alias(t, false, nil) // another server answers there, for now
	a1 := a.srv.URL
	if err := a.eng.SetAddress(ctx, a2.srv.URL); err != nil {
		t.Fatal(err)
	}
	if sv, _ := a.eng.Server(ctx); !sv.Waiting || sv.Named != a1 || sv.Former != a1 {
		t.Errorf("A: %+v", sv)
	}
	if _, err := b.eng.Check(ctx, bOnB.ID); err != nil {
		t.Fatal(err)
	}
	if x := a.lastExchange(); !strings.Contains(x.resBody, `"address":"`+a1+`"`) {
		t.Errorf("A's answer: %s", x.resBody)
	}
	for _, x := range b.exchanges("hello") {
		if strings.Contains(x.reqBody, a2.srv.URL) {
			t.Errorf("A2 named before it answers: %s", x.reqBody)
		}
	}
	if p, _ := b.eng.Peer(ctx, bOnB.ID); p.Address != a1 || p.Moving != nil {
		t.Errorf("A on B: %+v", p)
	}
	// 4 seconds: not tried again yet; 5: tried, A answers there now.
	a2.set(true)
	clock.Add(4 * time.Second)
	a.eng.RunOnce(ctx)
	if sv, _ := a.eng.Server(ctx); !sv.Waiting {
		t.Errorf("tried again too soon: %+v", sv)
	}
	clock.Add(time.Second)
	a.eng.RunOnce(ctx)
	waitAll(a, b)
	if p, _ := b.eng.Peer(ctx, bOnB.ID); p.Address != a2.srv.URL {
		t.Errorf("A on B: %+v", p)
	}
}

// During a move, A's hellos name the pins of both addresses, read from
// each; once it is over, A2's only.
func TestBothPinsDuringMove(t *testing.T) {
	ctx := context.Background()
	clock := newClock(1800000000)
	c1, c2 := selfSigned(t), selfSigned(t)
	pin1, pin2 := wire.Pin(c1.Leaf), wire.Pin(c2.Leaf)
	both := wire.CleanPins([]string{pin1, pin2}) // kept sorted
	a := newServerWith(t, msfp.Config{Clock: clock}, serverOpts{readPins: true, cert: &c1})
	b := newServer(t, msfp.Config{Clock: clock})
	cDial := &mapDialer{}
	c := newServer(t, msfp.Config{Clock: clock, Dialer: cDial})
	_, bOnB := pair(t, a, b)
	_, cOnC := pair(t, a, c)
	if p, _ := b.eng.Peer(ctx, bOnB.ID); !slices.Equal(peerRecord(t, b.store, p.ID).Pins, []string{pin1}) {
		t.Fatalf("A's pins on B: %v", peerRecord(t, b.store, p.ID).Pins)
	}
	a2 := a.alias(t, true, &c2)
	cDial.set(hostOf(a2.srv), closedAddr(t)) // C stays: the move lasts
	if err := a.eng.SetAddress(ctx, a2.srv.URL); err != nil {
		t.Fatal(err)
	}
	waitAll(a, b, c)
	if got := peerRecord(t, b.store, bOnB.ID).Pins; !slices.Equal(got, both) {
		t.Errorf("A's pins on B during the move: %v, want %v", got, both)
	}
	if _, err := c.eng.Check(ctx, cOnC.ID); err != nil {
		t.Fatal(err)
	}
	if got := peerRecord(t, c.store, cOnC.ID).Pins; !slices.Equal(got, both) {
		t.Errorf("A's pins on C during the move: %v", got)
	}
	clock.Add(31 * 24 * time.Hour)
	a.eng.RunOnce(ctx)
	if _, err := b.eng.Check(ctx, bOnB.ID); err != nil {
		t.Fatal(err)
	}
	if got := peerRecord(t, b.store, bOnB.ID).Pins; !slices.Equal(got, []string{pin2}) {
		t.Errorf("A's pins on B after the move: %v", got)
	}
}

// B moves to an address A never connects to (link-local): A keeps B's
// old address, and tries again later.
func TestMoveToRefusedDestination(t *testing.T) {
	ctx := context.Background()
	clock := newClock(1800000000)
	bDial := &mapDialer{}
	a := newServer(t, msfp.Config{Clock: clock}) // the engine's own Dialer
	b := newServer(t, msfp.Config{Clock: clock, Dialer: bDial})
	bOnA, _ := pair(t, a, b)
	b2 := b.alias(t, true, nil)
	bDial.set("169.254.10.10:8443", hostOf(b2.srv)) // B reaches itself there
	if err := b.eng.SetAddress(ctx, "https://169.254.10.10:8443"); err != nil {
		t.Fatal(err)
	}
	waitAll(a, b)
	if _, ok := has[msfp.AddressAnnounced](b.events); !ok {
		t.Fatal("B's new address not announced")
	}
	p, _ := a.eng.Peer(ctx, bOnA.ID)
	if p.Address != b.srv.URL || p.Moving == nil || p.Moving.Address != "https://169.254.10.10:8443" || p.Moving.FailingSince.IsZero() {
		t.Errorf("B on A: %+v %+v", p, p.Moving)
	}
	if _, err := a.eng.Check(ctx, bOnA.ID); err != nil {
		t.Errorf("B at its old address: %v", err)
	}
}

// B announces B2, which A can't reach yet; then B's key is revoked: A
// suspends B, and its move waits: A never calls B2, even once it could.
// A hello from B naming its stored address again drops a move.
func TestSuspendedMoveWaits(t *testing.T) {
	ctx := context.Background()
	clock := newClock(1800000000)
	aDial, bDial := &mapDialer{}, &mapDialer{}
	a := newServer(t, msfp.Config{Clock: clock, Dialer: aDial})
	b := newServer(t, msfp.Config{Clock: clock, Dialer: bDial})
	bOnA, aOnB := pair(t, a, b)
	b2 := b.alias(t, true, nil)
	aDial.set(hostOf(b2.srv), closedAddr(t))
	if err := b.eng.SetAddress(ctx, b2.srv.URL); err != nil {
		t.Fatal(err)
	}
	waitAll(a, b)
	if p, _ := a.eng.Peer(ctx, bOnA.ID); p.Moving == nil {
		t.Fatalf("B on A: %+v", p)
	}

	// B moves back to its first address: its hello names A's stored one
	// again, and the move is dropped.
	if err := b.eng.SetAddress(ctx, b.srv.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := b.eng.Check(ctx, aOnB.ID); err != nil {
		t.Fatal(err)
	}
	waitAll(a, b)
	if p, _ := a.eng.Peer(ctx, bOnA.ID); p.Moving != nil || p.Address != b.srv.URL {
		t.Errorf("B on A after its move back: %+v", p)
	}

	// To B2 again; then B's key revoked, with no next key.
	if err := b.eng.SetAddress(ctx, b2.srv.URL); err != nil {
		t.Fatal(err)
	}
	waitAll(a, b)
	if p, _ := a.eng.Peer(ctx, bOnA.ID); p.Moving == nil {
		t.Fatalf("B on A: %+v", p)
	}
	if _, err := b.eng.KeyStolen(ctx, "", true); err != nil {
		t.Fatal(err)
	}
	if p, _ := a.eng.Peer(ctx, bOnA.ID); p.Suspended.IsZero() {
		t.Fatalf("B on A: %+v", p)
	}
	aDial.set(hostOf(b2.srv), hostOf(b2.srv))
	before := len(b2.asked())
	clock.Add(2 * time.Hour)
	a.eng.RunOnce(ctx)
	waitAll(a, b)
	if got := b2.asked(); len(got) != before {
		t.Errorf("A called B2 while B is suspended: %v", got[before:])
	}
	if p, _ := a.eng.Peer(ctx, bOnA.ID); p.Address != b.srv.URL || p.Moving == nil {
		t.Errorf("B on A: %+v", p)
	}
}

// A's calls to B can't connect: B's next hello answer from A says so,
// and B shows it.
func TestCallsFailed(t *testing.T) {
	ctx := context.Background()
	clock := newClock(1800000000)
	aDial := &mapDialer{}
	a := newServer(t, msfp.Config{Clock: clock, Dialer: aDial})
	b := newServer(t, msfp.Config{Clock: clock})
	bOnA, aOnB := pair(t, a, b)
	aDial.set(hostOf(b.srv), closedAddr(t))
	b.srv.CloseClientConnections()
	if _, err := a.eng.Check(ctx, bOnA.ID); !errors.Is(err, msfp.ErrUnreachable) {
		t.Fatalf("A to B: %v", err)
	}
	if _, err := b.eng.Check(ctx, aOnB.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := has[msfp.CallsFailed](b.events); !ok {
		t.Error("no CallsFailed")
	}
	if p, _ := b.eng.Peer(ctx, aOnB.ID); !p.CallsFailed {
		t.Errorf("A on B: %+v", p)
	}
	// A reaches B again: the next answer clears it.
	aDial.set(hostOf(b.srv), hostOf(b.srv))
	if _, err := a.eng.Check(ctx, bOnA.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.eng.Check(ctx, aOnB.ID); err != nil {
		t.Fatal(err)
	}
	if p, _ := b.eng.Peer(ctx, aOnB.ID); p.CallsFailed {
		t.Errorf("A on B: %+v", p)
	}
}

// gateDialer holds the connections to one address until opened, and
// sends those to another to an address nothing listens at.
type gateDialer struct {
	held, closed string
	to           string
	waiting      chan struct{}
	open         chan struct{}
}

func (d *gateDialer) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	switch addr {
	case d.held:
		select {
		case d.waiting <- struct{}{}:
		default:
		}
		select {
		case <-d.open:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	case d.closed:
		addr = d.to
	}
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

// A peer that names a second new address while its first is checked:
// the first, once it answers under its key, is stored all the same, and
// the second is checked next (MSFP core section 13.2); it failing, the
// first stays.
func TestMoveNamedAgainWhileChecked(t *testing.T) {
	ctx := context.Background()
	b := newServer(t, msfp.Config{})
	b2, b3 := b.alias(t, true, nil), b.alias(t, true, nil)
	dial := &gateDialer{held: hostOf(b2.srv), closed: hostOf(b3.srv), to: closedAddr(t),
		waiting: make(chan struct{}, 1), open: make(chan struct{})}
	a := newServer(t, msfp.Config{Dialer: dial})
	bOnA, _ := pair(t, a, b)
	if err := b.eng.SetAddress(ctx, b2.srv.URL); err != nil {
		t.Fatal(err)
	}
	select {
	case <-dial.waiting:
	case <-time.After(10 * time.Second):
		t.Fatal("A never checked B2")
	}
	if err := b.eng.SetAddress(ctx, b3.srv.URL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		p, _ := a.eng.Peer(ctx, bOnA.ID)
		if p.Moving != nil && p.Moving.Address == b3.srv.URL {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("B3 never named to A: %+v", p.Moving)
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(dial.open)
	deadline = time.Now().Add(10 * time.Second)
	for {
		waitAll(a, b)
		p, _ := a.eng.Peer(ctx, bOnA.ID)
		if p.Address == b2.srv.URL && p.Moving != nil && p.Moving.Address == b3.srv.URL && !p.Moving.FailingSince.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("B on A: %s, moving %+v", p.Address, p.Moving)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
