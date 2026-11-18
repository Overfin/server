package msfp

import (
	"context"
	"time"

	"github.com/overfin/server/msfp/wire"
)

// The queries (engine.md section 2.2): copies the host may keep. They
// read storage, never call a peer.

// PeerView is what the panel shows of a peer.
type PeerView struct {
	ID, Name, Address string
	// Fingerprint is its current key's, for the operators to compare
	// (MSFP core section 4.2).
	Fingerprint string
	State       PeerState
	// Created, Paired and Seen are times (Paired and Seen zero for
	// never); Suspended is when it revoked its key (zero: it didn't).
	Created, Paired, Seen, Suspended time.Time
	// Core is the core version on with it, 0 when there is none in common
	// (the panel says the two can't talk); Parts the parts on, by name.
	Core  int
	Parts map[string]int
	// Inbound says whether it can be called; CallsFailed, that its latest
	// hello answer said our calls to it couldn't connect (suggest "this
	// server can't be called").
	Inbound     Inbound
	CallsFailed bool
	// NotMoved: it still calls this server's former address during a move
	// (MSFP core section 13.1); MoveLate: 3 days after the move was
	// announced. Moving is its own new address, being checked (nil for
	// none).
	NotMoved, MoveLate bool
	Moving             *MoveView
	// CommitmentFrom is where its next key commitment came from: "pair",
	// "rotation", or "" for none (its last rotation committed to none:
	// its next one waits for the operator).
	CommitmentFrom string
	// Pending is a new key of its waiting for the operator to confirm
	// (ConfirmRotation), At the statement's date; Refused a rotation
	// refused, at At (DismissRefused). nil for none.
	Pending, Refused *KeyNotice
	// OurKey is what it knows this server by: KeyCurrent, KeyOld (it
	// hasn't moved to our new key yet) or KeyLost (it never did: pair
	// again). RotationAnswer is its last answer to our rotate while on
	// the old key: "pending", "refused", or "".
	OurKey, RotationAnswer string
	// Limits are what it may use here; TheirLimits what it lets this
	// server use of it, as its last libraries answer said (nil until it
	// did).
	Limits      Limits
	TheirLimits *Limits
	// Offers is how many of the host's libraries are offered to it;
	// Wants how many of its libraries this server reads, WantsGone how
	// many of those it no longer offers.
	Offers, Wants, WantsGone int
}

// MoveView is a peer's new address, not confirmed yet (MSFP core section
// 13.2): since when it was announced, since when confirming it fails
// (zero: it hasn't), and Warn after a day of that.
type MoveView struct {
	Address             string
	Since, FailingSince time.Time
	Warn                bool
}

// KeyNotice is a peer's key the operator is shown: its fingerprint, and
// when.
type KeyNotice struct {
	Fingerprint string
	At          time.Time
}

// view is the PeerView of a record.
func (e *Engine) view(p *PeerRecord) PeerView {
	core, parts := e.agreed(p)
	v := PeerView{ID: p.ID, Name: p.Name, Address: p.Address, Fingerprint: wire.Fingerprint(p.Key),
		State: p.State, Created: p.Created, Paired: p.Paired, Seen: p.Seen, Suspended: p.Suspended,
		Core: core, Parts: parts, Inbound: p.Inbound, CommitmentFrom: p.CommitmentFrom,
		OurKey: p.OurKey, RotationAnswer: p.RotationAnswer, Limits: p.Limits, CallsFailed: p.CallsFailed}
	v.Offers = len(p.Offers)
	if p.TheirLimits != nil {
		l := *p.TheirLimits
		v.TheirLimits = &l
	}
	for _, w := range p.Wants {
		if w.Wanted {
			v.Wants++
			if w.Gone {
				v.WantsGone++
			}
		}
	}
	if a := e.address(); a.former != "" && a.reached && notMoved(p, wire.Host(a.address)) {
		v.NotMoved, v.MoveLate = true, e.now().Sub(a.movedAt) >= moveLate
	}
	if m := p.Move; m != nil {
		v.Moving = &MoveView{Address: m.Address, Since: m.Since, FailingSince: m.FailingSince,
			Warn: (wire.Move{Failing: m.FailingSince}).Warn(e.now())}
	}
	if p.Pending != nil {
		v.Pending = &KeyNotice{Fingerprint: wire.Fingerprint(p.Pending.New), At: time.Unix(p.Pending.Date, 0).UTC()}
	}
	if p.Refused != nil {
		v.Refused = &KeyNotice{Fingerprint: wire.Fingerprint(p.Refused.Key), At: p.Refused.At}
	}
	return v
}

// Peers lists every peer, in every state, by name.
func (e *Engine) Peers(ctx context.Context) ([]PeerView, error) {
	var out []PeerView
	err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		peers, err := tx.Peers()
		for i := range peers {
			out = append(out, e.view(&peers[i]))
		}
		return err
	})
	sortPeers(out)
	return out, err
}

// Peer is one peer. Error: ErrNotFound.
func (e *Engine) Peer(ctx context.Context, id string) (PeerView, error) {
	p, err := e.peerRecord(ctx, id)
	if err != nil {
		return PeerView{}, err
	}
	return e.view(p), nil
}

// ServerView is what the panel shows of this server.
type ServerView struct {
	// Fingerprint is the current key's.
	Fingerprint string
	// Address is where this server is now (its address of the .invalid
	// form while it can't be called), Named what its hellos name: its
	// former address while the new one isn't reached yet.
	Address, Named string
	// Former is the address it moved from, still accepted, MovedAt when
	// (zero: no move under way); Waiting, that the new address doesn't
	// answer yet, so peers aren't told; PeersMoved of PeersToMove (peers
	// that aren't gone) sign for the new one.
	Former                  string
	MovedAt                 time.Time
	Waiting                 bool
	PeersMoved, PeersToMove int
	// Uncallable is the operator's "this server can't be called"; Discovery,
	// that the discovery document is served.
	Uncallable, Discovery bool
	// NextKey is the committed next key's fingerprint (from its
	// commitment, MSFP core section 4.2), and NextOnServer whether its
	// seed is still here.
	NextKey      string
	NextOnServer bool
	// RotationSince is when this server's change of key began, zero when
	// none is under way; NotMoved, how many peers still know it by its
	// old key then (they must pair again if they haven't moved after 30
	// days).
	RotationSince time.Time
	NotMoved      int
	// RevokedAt is when this server's key was revoked with no next key
	// (KeyStolen withoutNext); for 30 days, former peers' hellos get the
	// revocation. Zero for none.
	RevokedAt time.Time
	// Core is the core version spoken, Parts the parts served.
	Core  []int
	Parts map[string][]int
}

// Server is this server, as the panel shows it.
func (e *Engine) Server(ctx context.Context) (ServerView, error) {
	r, err := e.serverRecord(ctx)
	if err != nil {
		return ServerView{}, err
	}
	v := e.versions()
	a := e.address()
	sv := ServerView{Fingerprint: wire.Fingerprint(e.current().Public()), Address: r.Address, Named: e.named(a),
		Uncallable: r.Uncallable, Discovery: e.discovery.Load(),
		NextKey: wire.CommitmentFingerprint(r.NextCommitment), NextOnServer: r.Next != nil,
		RevokedAt: r.RevokedAt, Core: v.Core, Parts: v.Parts}
	if r.Old != nil {
		sv.RotationSince = r.OldSince
	}
	if r.Former != "" {
		sv.MovedAt, sv.Waiting = r.MovedAt, !r.Reached
		if !wire.Uncallable(r.Former) {
			sv.Former = r.Former
		}
	}
	err = e.cfg.Store.Read(ctx, func(tx Tx) error {
		peers, err := tx.Peers()
		for i, p := range peers {
			if p.OurKey == KeyOld {
				sv.NotMoved++
			}
			if r.Former != "" && p.State != Gone {
				sv.PeersToMove++
				if !notMoved(&peers[i], wire.Host(r.Address)) {
					sv.PeersMoved++
				}
			}
		}
		return err
	})
	return sv, err
}
