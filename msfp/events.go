package msfp

import "log/slog"

// Event is something the operator's panel or the viewers' pages may want
// to show at once. Tell them apart with a type switch:
//
//	switch e := ev.(type) {
//	case msfp.PeerWaiting:
//		panel.Show("A server used your code: " + e.Name)
//	}
//
// Each event names the peer it concerns by ID ("" for an event of this
// server alone); its state is in the queries (Peer, Peers, Server) too,
// so a host that missed one loses nothing.
type Event interface {
	// PeerID is the peer the event concerns, "" for none.
	PeerID() string
}

// PeerWaiting: a peer redeemed our code. It is in state ours: the
// operator compares fingerprints, then accepts (Accept) or refuses
// (End). MSFP core section 7.1, step 4.
type PeerWaiting struct {
	Peer, Name, Fingerprint string
}

// PeerPaired: a pairing became paired, on either side (core section 7.1,
// step 6).
type PeerPaired struct{ Peer, Name string }

// PeerGone: a peer answered, signed, that it doesn't know us (core
// section 7.3); or it has been suspended 30 days without a valid
// rotation, and stays suspended (section 12.6).
type PeerGone struct{ Peer, Name string }

// PeerEnded: a pairing ended: by us (ByThem false, End) or by the peer
// (its hello said ended). Core section 7.4.
type PeerEnded struct {
	Peer, Name string
	ByThem     bool
}

// VersionsChanged: a peer's versions changed; the parts on with it may
// have changed (core section 10).
type VersionsChanged struct{ Peer, Name string }

// Keys (core section 12).

// PeerSuspended: a peer revoked its key. Nothing is heard from that key
// but rotate and revoke, and nothing is asked of the peer: show a
// warning, and hide what needs it from viewers (section 12.6).
type PeerSuspended struct{ Peer, Name string }

// PeerResumed: a suspended peer rotated to the key it had committed to:
// the suspension is lifted (section 12.6). PeerRotated comes with it.
type PeerResumed struct{ Peer, Name string }

// RotationPending: a peer moved to a key it hadn't committed to (its
// last rotation said none): the operator compares Fingerprint with the
// other operator, then calls ConfirmRotation, or End (section 12.3, case
// 4).
type RotationPending struct{ Peer, Name, Fingerprint string }

// RotationRefused: a peer's rotation to the key of Fingerprint didn't
// match its commitment, and was refused: someone holding its key tried
// to move the pairing to another key. A warning (section 12.3, case 3);
// DismissRefused clears it.
type RotationRefused struct{ Peer, Name, Fingerprint string }

// PeerRotated: a peer's new key, of Fingerprint, is current (section
// 12.3).
type PeerRotated struct{ Peer, Name, Fingerprint string }

// RotationAnswered: a peer answered this server's change of key: State
// "rotated" (it moved), "pending" (its operator must confirm) or
// "refused" (section 12.4).
type RotationAnswered struct{ Peer, Name, State string }

// OurKeyLost: the 30 days of this server's change of key are over (or a
// next change began) and this peer never moved: it must pair again
// (section 12.4). A warning.
type OurKeyLost struct{ Peer, Name string }

// RotationEnded: this server's change of key is over, its old key
// deleted: every peer moved, or Lost of them didn't (each told by
// OurKeyLost). An event of this server: PeerID is "".
type RotationEnded struct{ Lost int }

func (e PeerWaiting) PeerID() string      { return e.Peer }
func (e PeerPaired) PeerID() string       { return e.Peer }
func (e PeerGone) PeerID() string         { return e.Peer }
func (e PeerEnded) PeerID() string        { return e.Peer }
func (e VersionsChanged) PeerID() string  { return e.Peer }
func (e PeerSuspended) PeerID() string    { return e.Peer }
func (e PeerResumed) PeerID() string      { return e.Peer }
func (e RotationPending) PeerID() string  { return e.Peer }
func (e RotationRefused) PeerID() string  { return e.Peer }
func (e PeerRotated) PeerID() string      { return e.Peer }
func (e RotationAnswered) PeerID() string { return e.Peer }
func (e OurKeyLost) PeerID() string       { return e.Peer }
func (RotationEnded) PeerID() string      { return "" }

// notify tells the host events, in order, one at a time, outside the
// engine's locks: the first caller to find nobody telling becomes the
// one who tells, until none is left.
func (e *Engine) notify(events ...Event) {
	if len(events) == 0 {
		return
	}
	e.evMu.Lock()
	e.events = append(e.events, events...)
	if e.telling {
		e.evMu.Unlock()
		return
	}
	e.telling = true
	for len(e.events) > 0 {
		ev := e.events[0]
		e.events = e.events[1:]
		e.evMu.Unlock()
		e.tell(ev)
		e.evMu.Lock()
	}
	e.telling = false
	e.evMu.Unlock()
}

// tell is one event, to the Notifier, or the log when there's none. A
// crash in the host's Notifier is logged, not passed on.
func (e *Engine) tell(ev Event) {
	defer func() {
		if v := recover(); v != nil {
			e.log.Error("msfp: the notifier crashed", "event", slog.AnyValue(ev), "panic", v)
		}
	}()
	if e.cfg.Notifier == nil {
		e.log.Info("msfp: event", "event", ev)
		return
	}
	e.cfg.Notifier.Notify(ev)
}
