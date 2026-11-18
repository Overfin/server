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
// Each event names the peer it concerns by ID; its state is in the
// queries (Peer, Peers) too, so a host that missed one loses nothing.
type Event interface {
	// PeerID is the peer the event concerns.
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
// section 7.3).
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

func (e PeerWaiting) PeerID() string     { return e.Peer }
func (e PeerPaired) PeerID() string      { return e.Peer }
func (e PeerGone) PeerID() string        { return e.Peer }
func (e PeerEnded) PeerID() string       { return e.Peer }
func (e VersionsChanged) PeerID() string { return e.Peer }

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
