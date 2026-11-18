package msfp

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"time"

	"github.com/overfin/server/msfp/wire"
)

// Peers' changes of key, here (MSFP core sections 9.3, 9.4, 12.3 and
// 12.6): a rotate moves a peer to its new key at once when it matches the
// commitment held, waits for our operator when none is held, and is
// refused otherwise; a revoke suspends the key. Each is one Update on
// the peer's record, decided from the record as it is inside it.

// errRefused is a rotation refused: 409 rotation-refused.
var errRefused = errors.New("msfp: rotation refused")

// sha256Of is SHA-256 of b.
func sha256Of(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

// rotate is a peer telling it changed its key (MSFP core section 9.3).
func (e *Engine) rotate(req *request) response {
	if req.peer == nil {
		return plain404
	}
	rot, err := wire.ReadRotate(req.body, req.v.Key, e.now())
	if err != nil {
		return refuse(probInvalid)
	}
	state, err := e.receiveRotation(req.r.Context(), req.peer.ID, rot)
	switch {
	case errors.Is(err, errRefused):
		return refuse(probRotation)
	case errors.Is(err, ErrNotFound):
		return refuse(probNotFound)
	case err != nil:
		e.log.Error("msfp: a peer's change of key couldn't be kept", "peer", req.peer.ID, "error", err)
		return refuse(probInternal)
	case state == wire.StatePending:
		res := answer(stateAnswer{State: state})
		res.status = http.StatusAccepted
		return res
	}
	return answer(stateAnswer{State: state})
}

// receiveRotation applies a rotation of peer id, its statement checked
// (wire.ReadRotate), as MSFP core section 12.3 says: rotated, pending,
// or errRefused. A server that can't be called gets the same statement
// in a hello answer, and would apply it here too.
func (e *Engine) receiveRotation(ctx context.Context, id string, rot wire.Rotation) (string, error) {
	var state string
	var events []Event
	now := e.now().Truncate(time.Second)
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		state, events = "", nil
		p, err := tx.Peer(id)
		if err != nil || p == nil {
			return cmp.Or[error](err, ErrNotFound)
		}
		if !wire.SameKey(rot.Old, p.Key) {
			// Signed with the key it moved from, still heard: a retry of
			// the rotation that took effect changes nothing; any other is
			// refused.
			if wire.SameKey(rot.New, p.Key) {
				state = wire.StateRotated
			}
			return nil
		}
		// Not another peer's key, nor a forgotten one, nor one of its own
		// former keys: a retired key never becomes current again.
		if taken, err := keyTaken(tx, p, rot.New); err != nil || taken {
			return err
		}
		switch {
		case p.Commitment != nil && bytes.Equal(wire.Commitment(rot.New), p.Commitment):
			// The key it committed to: at once, a suspension lifted.
			events = rotatePeer(p, rot, now)
			state = wire.StateRotated
		case p.Commitment != nil || !p.Suspended.IsZero():
			// Someone holding its key tried to move the pairing to another
			// key: refused, kept for our operator.
			p.Refused = &RefusedKey{Key: rot.New, At: now}
			events = []Event{RotationRefused{Peer: p.ID, Name: p.Name, Fingerprint: wire.Fingerprint(rot.New)}}
		default:
			// No commitment (its last statement said "-"): our operator
			// confirms the new key's fingerprint with the other operator.
			// Repeated, the newest statement is kept; another key
			// replaces it.
			if p.Pending == nil || !wire.SameKey(p.Pending.New, rot.New) {
				events = []Event{RotationPending{Peer: p.ID, Name: p.Name, Fingerprint: wire.Fingerprint(rot.New)}}
			}
			p.Pending, state = &rot, wire.StatePending
		}
		return tx.PutPeer(*p)
	})
	if err != nil {
		return "", err
	}
	for _, ev := range events {
		switch ev.(type) {
		case PeerRotated:
			e.log.Info("msfp: a peer changed its key", "peer", id, "fingerprint", wire.Fingerprint(rot.New))
		case PeerResumed:
			e.log.Warn("msfp: a peer whose key was revoked moved to the key it had committed to: no longer suspended", "peer", id)
		case RotationRefused:
			e.log.Warn("msfp: a peer's change of key refused: the new key isn't the one it committed to", "peer", id,
				"fingerprint", wire.Fingerprint(rot.New))
		}
	}
	e.notify(events...)
	if state == "" {
		return "", errRefused
	}
	return state, nil
}

// rotatePeer makes rot.New p's current key (MSFP core section 12.3,
// step 5): its old key the previous one, heard for 10 minutes unless it
// was revoked (a revoked key is never heard again, section 12.6); the
// statement's commitment its commitment; the statement kept. A
// suspension lifts; a pending rotation and a refused one are dropped.
func rotatePeer(p *PeerRecord, rot wire.Rotation, now time.Time) []Event {
	events := []Event{PeerRotated{Peer: p.ID, Name: p.Name, Fingerprint: wire.Fingerprint(rot.New)}}
	p.PreviousKey, p.PreviousUntil = p.Key, now.Add(previousHeard)
	if !p.Suspended.IsZero() {
		p.PreviousUntil, p.Suspended = time.Time{}, time.Time{}
		events = append(events, PeerResumed{Peer: p.ID, Name: p.Name})
	}
	p.Key, p.Commitment, p.CommitmentFrom = rot.New, rot.Next, "rotation"
	if rot.Next == nil {
		p.CommitmentFrom = ""
	}
	p.Pending, p.Refused = nil, nil
	p.Rotations = append(p.Rotations, rot)
	return events
}

// keyTaken says whether p may not move to key (MSFP core section 12.3,
// step 1): another peer's key, a forgotten one, or one of p's former
// keys (its previous key, any key of its rotation statements).
func keyTaken(tx Tx, p *PeerRecord, key []byte) (bool, error) {
	other, err := tx.PeerByKey(key)
	if err != nil || other != nil && other.ID != p.ID {
		return other != nil, err
	}
	f, err := tx.Forgotten(sha256Of(key))
	if err != nil || f != nil {
		return f != nil, err
	}
	if bytes.Equal(p.PreviousKey, key) {
		return true, nil
	}
	for _, r := range p.Rotations {
		if bytes.Equal(r.Old, key) || bytes.Equal(r.New, key) {
			return true, nil
		}
	}
	return false, nil
}

// revoke is a peer saying its key is stolen (MSFP core sections 9.4 and
// 12.6).
func (e *Engine) revoke(req *request) response {
	if req.peer == nil {
		return plain404
	}
	if _, err := wire.ReadRevoke(req.body, req.v.Key, e.now()); err != nil {
		return refuse(probInvalid)
	}
	ev, err := e.revoked(req.r.Context(), req.peer.ID, req.v.Key)
	if err != nil {
		return refuse(probInternal)
	}
	e.notify(ev...)
	return answer(stateAnswer{State: wire.StateSuspended})
}

// revoked applies a valid revocation of key, a key of peer id: its
// current key, which is suspended (already suspended: nothing changes),
// or its previous one, no longer heard at once.
func (e *Engine) revoked(ctx context.Context, id string, key []byte) ([]Event, error) {
	var events []Event
	err := e.updatePeer(ctx, id, func(p *PeerRecord) {
		events = nil
		switch {
		case !wire.SameKey(key, p.Key):
			// Its previous key: no longer heard; the peer, known by its
			// current key, isn't concerned.
			p.PreviousUntil = time.Time{}
		case p.Suspended.IsZero():
			p.Suspended = e.now().Truncate(time.Second)
			e.log.Warn("msfp: a peer revoked its key: suspended until it moves to the key it committed to", "peer", p.ID, "name", p.Name)
			events = []Event{PeerSuspended{Peer: p.ID, Name: p.Name}}
		}
	})
	return events, err
}

// ConfirmRotation is this server's operator confirming a peer's pending
// new key, after comparing its fingerprint with the other operator (MSFP
// core section 12.3, case 4): it becomes the peer's key, and the peer is
// sent a hello at once, which makes it send its rotate again and see it
// rotated. Errors: ErrNotFound, ErrSuspended, ErrNoPending, ErrKeyTaken.
func (e *Engine) ConfirmRotation(ctx context.Context, id string) (PeerView, error) {
	var events []Event
	now := e.now().Truncate(time.Second)
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		p, err := tx.Peer(id)
		switch {
		case err != nil:
			return err
		case p == nil:
			return ErrNotFound
		case !p.Suspended.IsZero():
			return ErrSuspended
		case p.Pending == nil || !wire.SameKey(p.Pending.Old, p.Key):
			return ErrNoPending
		}
		if taken, err := keyTaken(tx, p, p.Pending.New); err != nil || taken {
			return cmp.Or[error](err, ErrKeyTaken)
		}
		events = rotatePeer(p, *p.Pending, now)
		return tx.PutPeer(*p)
	})
	if err != nil {
		return PeerView{}, err
	}
	e.log.Info("msfp: a peer's new key confirmed by the operator", "peer", id)
	e.notify(events...)
	p, err := e.peerRecord(ctx, id)
	if err != nil {
		return PeerView{}, err
	}
	if p.Inbound == InboundYes {
		// Its answer may still be signed with the old key, and then
		// doesn't count: what matters is the rotate it sends again.
		if _, err := e.Check(ctx, id); err != nil {
			e.log.Info("msfp: the peer couldn't be told its new key was confirmed; it is at its next contact", "peer", id, "error", err)
		}
	}
	return e.Peer(ctx, id)
}

// DismissRefused clears the warning of a peer's refused rotation, once
// the operator saw it. Error: ErrNotFound.
func (e *Engine) DismissRefused(ctx context.Context, id string) error {
	return e.updatePeer(ctx, id, func(p *PeerRecord) { p.Refused = nil })
}

// revocationAnswer is, for the 30 days after this server's key was
// revoked with no next key (MSFP core section 12.7, case 2), the answer
// to a hello from a peer whose pairing ended in that replacement: a 200
// signed with the key revoked, carrying only the revocation, newly
// dated. ok is false for anyone else.
func (e *Engine) revocationAnswer(req *request) (response, bool) {
	k := e.ourKeys()
	if k.revoked == nil || e.now().Sub(k.revokedAt) >= keyOverlap {
		return response{}, false
	}
	var f *ForgottenKey
	if err := e.cfg.Store.Read(req.r.Context(), func(tx Tx) error {
		var err error
		f, err = tx.Forgotten(sha256Of(req.v.Key))
		return err
	}); err != nil || f == nil || !f.Revoked || !f.At.Equal(k.revokedAt) {
		return response{}, false
	}
	req.signer = k.revoked
	return answer(revocationOnly{Revocation: wire.SignRevocation(*k.revoked, e.now()).Body()}), true
}

// revocationOnly is the answer of revocationAnswer.
type revocationOnly struct {
	Revocation wire.RevokeBody `json:"revocation"`
}

// pullable adds to a hello answer to p, a peer that can't be called,
// what it can't be sent otherwise (MSFP core sections 12.4 and 12.6):
// while p knows us by our old key, our rotation statement, newly dated,
// and when that key was revoked, the revocation, newly dated too. Both
// travel only in answers signed with the old key.
func (e *Engine) pullable(p *PeerRecord, h *helloMessage) {
	k := e.ourKeys()
	if p.Inbound != InboundNo || p.OurKey != KeyOld || k.old == nil {
		return
	}
	now := e.now()
	rot := wire.SignRotation(*k.old, k.current, k.commitment, now).Body()
	h.Rotation = &rot
	if k.oldRevoked {
		rev := wire.SignRevocation(*k.old, now).Body()
		h.Revocation = &rev
	}
}

// knownKey reads, from the hello of p, a peer that can't be called, which
// of our keys it knows us by (MSFP core section 12.4, step 4): during a
// change, the new one means p moved, and the answer is signed with it
// already; the old one, with pending or refused, is its answer to our
// rotation. A key that is neither of ours is ignored.
func (e *Engine) knownKey(ctx context.Context, req *request, p *PeerRecord, h wire.Hello) *PeerRecord {
	k := e.ourKeys()
	if p.OurKey != KeyOld || h.KnownKey == nil || k.old == nil {
		return p
	}
	switch {
	case wire.SameKey(h.KnownKey, k.current.Public()):
		if e.peerMoved(ctx, p.ID, h.KnownKey) {
			req.signer = &k.current
		}
	case wire.SameKey(h.KnownKey, k.old.Public()) && h.RotationState != "":
		e.rotationAnswer(ctx, p.ID, k.current.Public(), h.RotationState)
	default:
		return p
	}
	if q, err := e.peerRecord(ctx, p.ID); err == nil {
		return q
	}
	return p
}
