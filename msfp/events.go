package msfp

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

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

// Addresses (core sections 7.5 and 13).

// AddressAnnounced: this server reached itself at its new address, and
// told its peers: they move to it as they confirm it (section 13.1,
// step 2). An event of this server: PeerID is "".
type AddressAnnounced struct{ Address string }

// MoveEnded: this server's move is over, its former address no longer
// accepted: every peer moved (NotMoved 0), or 30 days passed and
// NotMoved of them hadn't (section 13.1). An event of this server.
type MoveEnded struct{ NotMoved int }

// NotMovedLate: a peer still calls this server's former address 3 days
// after the move was announced. A warning (section 13.1).
type NotMovedLate struct{ Peer, Name string }

// PeerMoved: a peer's new address was confirmed (or, for a peer that
// can't be called, read from its hello) and stored (section 13.2).
type PeerMoved struct{ Peer, Name, From, To string }

// PeerMoveFailing: a peer's new address has failed to answer as it for
// a day: it is still called at its old one (section 13.2, step 3). A
// warning, once per move.
type PeerMoveFailing struct {
	Peer, Name, Address string
	Since               time.Time
}

// InboundChanged: a peer says it can't be called now (Inbound InboundNo),
// or can again (InboundConfirming: its address is checked first), or its
// address answered and it is called again (InboundYes); section 7.5.
type InboundChanged struct {
	Peer, Name string
	Inbound    Inbound
}

// CallsFailed: a peer's hello answer said this server's last calls to it
// couldn't connect: the panel may suggest "this server can't be called"
// (section 7.5). Nothing else changes.
type CallsFailed struct{ Peer, Name string }

// Catalogs and playbacks.

// CatalogReadEnded: a reading of a peer's titles ended: Titles kept (new or
// changed, for a reading of changes), or Err, why it failed; what was
// kept before a failed reading stays (MSFP catalog section 3.2).
type CatalogReadEnded struct {
	Peer, Name string
	Titles     int
	Changes    bool
	Err        error
}

// WantsGone: libraries this server wants of a peer (their public IDs)
// weren't in its last libraries answer: it no longer offers them (MSFP
// catalog section 2.2). Their titles go with the next reading.
type WantsGone struct {
	Peer, Name string
	Libraries  []string
}

// PlaybackStarted: a peer's playback of Title started here (MSFP
// playback section 6.1), for the host's live view.
type PlaybackStarted struct {
	Peer, Name, Playback, Title, Method string
}

// PlaybackEnded: a peer's playback here ended, and why.
type PlaybackEnded struct {
	Peer, Name, Playback, Title string
	Reason                      EndReason
}

// RemotePlaybackEnded: a playback of this server's on a peer ended
// without the host's Stop: the peer answered 410 (Reason EndExpired),
// the pairing ended, the peer was suspended or can't be called
// (EndPairingEnded). Its fetches fail from then on; the viewer's player
// stops.
type RemotePlaybackEnded struct {
	Peer, Name, Playback string
	Reason               EndReason
}

func (e PeerWaiting) PeerID() string         { return e.Peer }
func (e PeerPaired) PeerID() string          { return e.Peer }
func (e PeerGone) PeerID() string            { return e.Peer }
func (e PeerEnded) PeerID() string           { return e.Peer }
func (e VersionsChanged) PeerID() string     { return e.Peer }
func (e PeerSuspended) PeerID() string       { return e.Peer }
func (e PeerResumed) PeerID() string         { return e.Peer }
func (e RotationPending) PeerID() string     { return e.Peer }
func (e RotationRefused) PeerID() string     { return e.Peer }
func (e PeerRotated) PeerID() string         { return e.Peer }
func (e RotationAnswered) PeerID() string    { return e.Peer }
func (e OurKeyLost) PeerID() string          { return e.Peer }
func (RotationEnded) PeerID() string         { return "" }
func (AddressAnnounced) PeerID() string      { return "" }
func (MoveEnded) PeerID() string             { return "" }
func (e NotMovedLate) PeerID() string        { return e.Peer }
func (e PeerMoved) PeerID() string           { return e.Peer }
func (e PeerMoveFailing) PeerID() string     { return e.Peer }
func (e InboundChanged) PeerID() string      { return e.Peer }
func (e CallsFailed) PeerID() string         { return e.Peer }
func (e CatalogReadEnded) PeerID() string    { return e.Peer }
func (e WantsGone) PeerID() string           { return e.Peer }
func (e PlaybackStarted) PeerID() string     { return e.Peer }
func (e PlaybackEnded) PeerID() string       { return e.Peer }
func (e RemotePlaybackEnded) PeerID() string { return e.Peer }

// notify tells the host events, in order, one at a time, outside the
// engine's locks: the first caller to find nobody telling becomes the
// one who tells, until none is left.
func (e *Engine) notify(events ...Event) {
	if len(events) == 0 {
		return
	}
	for _, ev := range events {
		e.react(ev)
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

// react is what the engine itself does on an event, before the host is
// told: a peer no longer paired, or suspended, ends its playbacks both
// ways (MSFP playback section 6.3); one that can't be called is fetched
// from no more, and what was kept of its titles goes (MSFP core section
// 7.5); one that can be called again is read at once.
func (e *Engine) react(ev Event) {
	switch ev := ev.(type) {
	case PeerEnded, PeerGone, PeerSuspended:
		id := ev.PeerID()
		e.endPlaybacks(func(pb *playback) bool { return pb.peer == id }, EndPairingEnded)
		e.endRemotes(id, EndPairingEnded)
		if _, gone := ev.(PeerGone); gone {
			e.background(func(ctx context.Context) { e.dropCatalog(ctx, id) })
		}
	case InboundChanged:
		if ev.Inbound == InboundNo {
			e.endRemotes(ev.Peer, EndPairingEnded)
			e.background(func(ctx context.Context) { e.dropCatalog(ctx, ev.Peer) })
		}
	case VersionsChanged:
		// The parts on may have changed: the next reading of this
		// server's titles is a whole one.
		id := ev.Peer
		e.background(func(ctx context.Context) {
			mark, err := e.seqMark(ctx)
			if err == nil {
				err = e.cfg.Store.Update(ctx, func(tx Tx) error {
					p, err := tx.Peer(id)
					if err != nil || p == nil {
						return err
					}
					if p.OffersSeq, err = e.offersSeq(tx, mark); err != nil {
						return err
					}
					return tx.PutPeer(*p)
				})
			}
			if err == nil {
				e.bumpLog(ctx)
			}
			if err != nil && !errors.Is(err, ErrNotFound) {
				e.log.Warn("msfp: a peer's change of versions couldn't be noted", "peer", id, "error", err)
			}
		})
	}
}
