package msfp

import (
	"bytes"
	"cmp"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/overfin/server/msfp/wire"
)

// This server's changes of key (MSFP core section 12).
//
// The server keeps a next key ready, made in advance; its peers hold the
// commitment to it, from pair or from a rotation statement. Rotate makes
// the next key current: every peer is sent a rotate, signed with the old
// key, and moves at once since the new key matches the commitment. Until
// a peer answered rotated, the engine signs toward it with the old key,
// and tells it again at each contact and at least daily; after 30 days
// the old key is deleted, and a peer that didn't move must pair again.
//
// KeyStolen revokes the current key: every peer it reaches suspends it.
// With the next key, the server rotates to it in the same action, and
// peers holding its commitment follow. Without it, the server makes a new
// key and every pairing ends.
//
// One owner. Every change of this server's keys is one Update on the
// server record (changeServer), which decides from the record as it is
// inside that transaction: a rotation ends only if its old key is still
// the one kept (finishRotation), and a peer is marked as moved only to
// the key that is still current, the last one to move ending the change
// in the same transaction (peerMoved). So a change learnt late (a peer's
// answer, the hourly round's 30 days) never touches the change begun
// after it. changeServer also puts the keys it committed in effect before
// the next write of the record can start, so the keys in memory are
// always the record's as last committed.

// The calls of key changes (MSFP core sections 9.3 and 9.4).
const (
	callRotate = "rotate"
	callRevoke = "revoke"
)

// The values of PeerRecord.OurKey: what a peer knows this server by.
const (
	// KeyCurrent: our current key.
	KeyCurrent = ""
	// KeyOld: our key before the current one; it hasn't moved yet.
	KeyOld = "old"
	// KeyLost: it never moved, and our old key is deleted: it must
	// pair again.
	KeyLost = "lost"
)

// Times of key changes (MSFP core sections 12.4 and 12.6), fixed.
const (
	// keyOverlap is how long a change of key lasts at most, how long a
	// peer stays suspended before it is gone, and how long a key revoked
	// with no next key answers former peers' hellos.
	keyOverlap = 30 * 24 * time.Hour
	// previousHeard is how long a peer's key is still heard after it
	// rotated away from it: requests already on the way.
	previousHeard = 10 * time.Minute
	// rotateDaily is how often, at least, a peer on our old key is told
	// again.
	rotateDaily = 24 * time.Hour
	// rotateSpacing is the least time between two rotates sent at
	// contacts; rotatePendingSpacing the same toward a peer whose
	// operator is to confirm our key: its call may be the first since
	// it confirmed, and its calls fail until it is told again.
	rotateSpacing        = time.Minute
	rotatePendingSpacing = 10 * time.Second
)

// The next key (MSFP core section 12.5).

// nextKeyPrefix starts an exported next key; MSFP's one form.
const nextKeyPrefix = "msfp-next-key-1."

// NextKey is the committed next key's fingerprint (made from its
// commitment, MSFP core section 4.2), and whether its seed is still on
// this server.
func (e *Engine) NextKey(ctx context.Context) (fingerprint string, onServer bool, err error) {
	r, err := e.serverRecord(ctx)
	if err != nil {
		return "", false, err
	}
	return wire.CommitmentFingerprint(r.NextCommitment), r.Next != nil, nil
}

// ExportNextKey gives the next key's seed as text to keep offline
// ("msfp-next-key-1." then the seed, base64url), and its fingerprint for
// the operator to compare. Error: ErrNoNextKey.
func (e *Engine) ExportNextKey(ctx context.Context) (text, fingerprint string, err error) {
	r, err := e.serverRecord(ctx)
	if err != nil {
		return "", "", err
	}
	if r.Next == nil {
		return "", "", ErrNoNextKey
	}
	seed, err := e.cfg.Sealer.Open(r.Next, sealNext)
	if err != nil {
		return "", "", ErrSealed.with(err)
	}
	return nextKeyPrefix + wire.Encode64(seed), wire.CommitmentFingerprint(r.NextCommitment), nil
}

// DeleteNextKey removes the next key's seed from this server, once the
// operator exported it; its commitment stays. Someone who copies the
// server's storage then can't change its key. Rotate and KeyStolen then
// need the exported text. Error: ErrNoNextKey.
func (e *Engine) DeleteNextKey(ctx context.Context) error {
	return e.changeServer(ctx, func(_ Tx, r *ServerRecord) error {
		if r.Next == nil {
			return ErrNoNextKey
		}
		r.Next = nil
		return nil
	})
}

// nextKey is the key to rotate to: the exported text given back, or,
// for "", the seed on the server. Errors: ErrNotNextKey (a text that
// isn't the committed key, or not in MSFP's form), ErrNoNextKey (no
// seed here, no text).
func (e *Engine) nextKey(ctx context.Context, text string) (wire.Key, error) {
	r, err := e.serverRecord(ctx)
	if err != nil {
		return wire.Key{}, err
	}
	if strings.TrimSpace(text) != "" {
		// White space anywhere is removed; any other prefix refused.
		rest, ok := strings.CutPrefix(strings.Join(strings.Fields(text), ""), nextKeyPrefix)
		seed, good := wire.Decode64(rest, 32)
		if !ok || !good {
			return wire.Key{}, ErrNotNextKey
		}
		k, err := wire.KeyFromSeed(seed)
		if err != nil || !bytes.Equal(wire.Commitment(k.Public()), r.NextCommitment) {
			return wire.Key{}, ErrNotNextKey
		}
		return k, nil
	}
	if r.Next == nil {
		return wire.Key{}, ErrNoNextKey
	}
	return e.openKey(r.Next, sealNext)
}

// The operator's actions.

// Rotate moves this server to its next key: the seed on the server
// (nextKey ""), or the exported text given back. Every peer that can be
// called is sent a rotate at once; Rotate returns when each has answered
// or failed. A peer that can't be called learns it in the answers to its
// hellos. A change still under way is finished first: peers that hadn't
// moved from the key before must pair again (MSFP core section 12.4).
// Errors: ErrNoNextKey, ErrNotNextKey, ErrRotationUnderWay (another key
// action is running now).
func (e *Engine) Rotate(ctx context.Context, nextKey string) error {
	if !e.keyAction.TryLock() {
		return ErrRotationUnderWay
	}
	defer e.keyAction.Unlock()
	next, err := e.nextKey(ctx, nextKey)
	if err != nil {
		return err
	}
	if err := e.rotateTo(ctx, next, false); err != nil {
		return err
	}
	e.tellRotation(ctx)
	return nil
}

// KeyStolen is this server's operator saying its key was stolen (MSFP
// core section 12.7). The key is revoked: every peer that can be called
// is sent a revoke, and suspends it. Then, unless withoutNext, the server
// rotates to its next key in the same action (the seed on the server, or
// the exported text given back), and peers holding the commitment follow:
// rotated is true.
//
// withoutNext, when the next key may be stolen too or is lost: the server
// makes a new key, not by a rotation, and every pairing ends (case 2):
// peers are deleted, their keys forgotten, and no ended is sent, which
// they would refuse from a suspended key. For 30 days, a hello from one
// of them is answered with the revocation, signed with the key revoked,
// so a peer revoke didn't reach learns it too. Each pairing is then made
// again with a new code.
//
// Errors: ErrNoNextKey (no seed here and no text, without withoutNext:
// nothing is revoked), ErrNotNextKey, ErrRotationUnderWay.
func (e *Engine) KeyStolen(ctx context.Context, nextKey string, withoutNext bool) (rotated bool, err error) {
	if !e.keyAction.TryLock() {
		return false, ErrRotationUnderWay
	}
	defer e.keyAction.Unlock()
	var next wire.Key
	if !withoutNext {
		if next, err = e.nextKey(ctx, nextKey); err != nil {
			return false, err
		}
	}
	// A change under way is finished first: every peer left then knows
	// us by the key revoked.
	if k := e.ourKeys(); k.old != nil {
		if _, err := e.finishRotation(ctx, k.old.Public()); err != nil {
			return false, err
		}
	}
	stolen := e.current()
	e.log.Warn("msfp: this server's key revoked", "fingerprint", wire.Fingerprint(stolen.Public()))
	statement := wire.SignRevocation(stolen, e.now())
	e.eachPeer(ctx, func(p *PeerRecord) bool { return p.OurKey == KeyCurrent && p.Inbound == InboundYes },
		func(ctx context.Context, p *PeerRecord) {
			var ans stateAnswer
			if r, err := e.call(ctx, p, callRevoke, statement.Body(), &ans, false); err != nil || r.status != http.StatusOK {
				e.log.Info("msfp: a peer couldn't be told this server's key is revoked", "peer", p.ID, "status", r.status, "error", err)
			}
		})
	if !withoutNext {
		if err := e.rotateTo(ctx, next, true); err != nil {
			return false, err
		}
		e.tellRotation(ctx)
		return true, nil
	}
	return false, e.replaceKey(ctx, stolen)
}

// rotateTo makes next, the committed next key, current, keeps the
// current one as the old key, and makes a new next key, all in one
// Update; every peer that knew us by the current key is then on the old
// one. revoked: the current key was just revoked. A change under way
// ends in the same Update. Live codes are withdrawn: they name the old
// key, which a pair answer is no longer signed with.
func (e *Engine) rotateTo(ctx context.Context, next wire.Key, revoked bool) error {
	afterSeed, err := e.random(32)
	if err != nil {
		return err
	}
	after, err := wire.KeyFromSeed(afterSeed)
	if err != nil {
		return ErrInternal.with(err)
	}
	now := e.now().Truncate(time.Second)
	var events []Event
	err = e.changeServer(ctx, func(tx Tx, r *ServerRecord) error {
		events = nil
		if !bytes.Equal(wire.Commitment(next.Public()), r.NextCommitment) {
			return ErrNotNextKey // another rotation took it meanwhile
		}
		if r.Old != nil {
			ev, err := e.endRotation(tx, r)
			if err != nil {
				return err
			}
			events = ev
		}
		seed, err := e.cfg.Sealer.Open(r.Current, sealCurrent)
		if err != nil {
			return ErrSealed.with(err)
		}
		if r.Old, err = e.cfg.Sealer.Seal(seed, sealOld); err != nil {
			return err
		}
		if r.Current, err = e.cfg.Sealer.Seal(next.Seed(), sealCurrent); err != nil {
			return err
		}
		if r.Next, err = e.cfg.Sealer.Seal(after.Seed(), sealNext); err != nil {
			return err
		}
		r.OldSince, r.OldRevoked, r.NextCommitment = now, revoked, wire.Commitment(after.Public())
		peers, err := tx.Peers()
		if err != nil {
			return err
		}
		for _, p := range peers {
			if p.OurKey != KeyCurrent {
				continue
			}
			p.OurKey, p.RotationAnswer, p.RotateSent, p.CommitmentTold = KeyOld, "", time.Time{}, false
			if err := tx.PutPeer(p); err != nil {
				return err
			}
		}
		return withdrawCodes(tx)
	})
	if err != nil {
		return err
	}
	e.log.Info("msfp: this server's key changed", "to", wire.Fingerprint(next.Public()))
	e.tellEnded(events)
	return nil
}

// withdrawCodes deletes every code.
func withdrawCodes(tx Tx) error {
	codes, err := tx.Codes()
	if err != nil {
		return err
	}
	for _, c := range codes {
		if err := tx.DeleteCode(c.ID); err != nil {
			return err
		}
	}
	return nil
}

// replaceKey gives this server a new key and a new next key, with no
// rotation, after stolen was revoked with no next key (MSFP core section
// 12.7, case 2), in one Update: every peer deleted, each key of its
// history forgotten as ended by this replacement (ForgottenKey.Revoked,
// at RevokedAt), the revoked key kept for its 30 days of answers.
func (e *Engine) replaceKey(ctx context.Context, stolen wire.Key) error {
	fresh, err := e.random(32)
	if err != nil {
		return err
	}
	afterSeed, err := e.random(32)
	if err != nil {
		return err
	}
	after, err := wire.KeyFromSeed(afterSeed)
	if err != nil {
		return ErrInternal.with(err)
	}
	now := e.now().Truncate(time.Second)
	var ended []PeerRecord
	err = e.changeServer(ctx, func(tx Tx, r *ServerRecord) error {
		ended = nil
		cur, err := e.openKey(r.Current, sealCurrent)
		if err != nil {
			return err
		}
		if !wire.SameKey(cur.Public(), stolen.Public()) {
			return ErrRotationUnderWay // changed meanwhile: nothing is replaced
		}
		peers, err := tx.Peers()
		if err != nil {
			return err
		}
		for _, p := range peers {
			if err := forgetPeer(tx, &p, now, true); err != nil {
				return err
			}
			ended = append(ended, p)
		}
		if r.Revoked, err = e.cfg.Sealer.Seal(stolen.Seed(), sealOld); err != nil {
			return err
		}
		if r.Current, err = e.cfg.Sealer.Seal(fresh, sealCurrent); err != nil {
			return err
		}
		if r.Next, err = e.cfg.Sealer.Seal(after.Seed(), sealNext); err != nil {
			return err
		}
		r.RevokedAt, r.NextCommitment = now, wire.Commitment(after.Public())
		r.Old, r.OldSince, r.OldRevoked = nil, time.Time{}, false
		return withdrawCodes(tx)
	})
	if err != nil {
		return err
	}
	e.log.Warn("msfp: this server has a new key, without its next one: every pairing ended, to be made again",
		"fingerprint", wire.Fingerprint(e.current().Public()), "peers", len(ended))
	events := make([]Event, 0, len(ended))
	for _, p := range ended {
		events = append(events, PeerEnded{Peer: p.ID, Name: p.Name})
	}
	e.notify(events...)
	return nil
}

// forgetPeer deletes p in tx, every key of its history forgotten at now
// (MSFP core section 7.4, step 1); byReplacement marks them ended by
// replaceKey.
func forgetPeer(tx Tx, p *PeerRecord, now time.Time, byReplacement bool) error {
	for _, k := range historyKeys(p) {
		hash := sha256Of(k)
		if err := tx.Forget(ForgottenKey{KeyHash: hash, At: now, Revoked: byReplacement}); err != nil {
			return err
		}
	}
	return tx.DeletePeer(p.ID)
}

// The rotating side, toward each peer (MSFP core section 12.4).

// stateAnswer is the answer of rotate and revoke.
type stateAnswer struct {
	State string `json:"state"`
}

// tellRotation sends a rotate to every peer on our old key that can be
// called, all at once, until each answered or failed.
func (e *Engine) tellRotation(ctx context.Context) {
	e.eachPeer(ctx, func(p *PeerRecord) bool { return p.OurKey == KeyOld && p.Inbound == InboundYes },
		func(ctx context.Context, p *PeerRecord) { e.rotateIfDue(ctx, p.ID, 0, false) })
}

// eachPeer runs do for every peer keep says, all at once, and waits.
func (e *Engine) eachPeer(ctx context.Context, keep func(*PeerRecord) bool, do func(context.Context, *PeerRecord)) {
	var peers []PeerRecord
	if err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		var err error
		peers, err = tx.Peers()
		return err
	}); err != nil {
		e.log.Warn("msfp: the peers couldn't be read", "error", err)
		return
	}
	var wg sync.WaitGroup
	for i := range peers {
		if keep(&peers[i]) {
			wg.Go(func() { do(ctx, &peers[i]) })
		}
	}
	wg.Wait()
}

// rotateIfDue sends peer id a rotate, newly dated, unless one went less
// than spacing ago (0: whatever the last), and gives the peer as it is
// then (nil when it is gone). Of contacts at once, one only sends it: the
// time sent is claimed in an Update first.
func (e *Engine) rotateIfDue(ctx context.Context, id string, spacing time.Duration, background bool) *PeerRecord {
	now := e.now()
	var p PeerRecord
	claimed := false
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		claimed = false
		q, err := tx.Peer(id)
		if err != nil || q == nil {
			return cmp.Or[error](err, ErrNotFound)
		}
		p = *q
		if q.OurKey != KeyOld || !q.Suspended.IsZero() || spacing > 0 && now.Sub(q.RotateSent) < spacing {
			return nil
		}
		q.RotateSent, claimed = now, true
		p = *q
		return tx.PutPeer(*q)
	})
	if err != nil {
		return nil
	}
	// One copy of the keys, read after the claim: the statement names our
	// old and current keys of the same change, and its answer is applied
	// to that change only (rotateAnswered).
	k := e.ourKeys()
	if !claimed || k.old == nil {
		return &p
	}
	rot := wire.SignRotation(*k.old, k.current, k.commitment, now)
	var ans stateAnswer
	r, err := e.call(ctx, &p, callRotate, rot.Body(), &ans, background)
	e.rotateAnswered(ctx, &p, k.current.Public(), r, ans.State, err)
	q, _ := e.peerRecord(ctx, id)
	return q
}

// rotateAnswered applies p's answer to our rotate to current.
func (e *Engine) rotateAnswered(ctx context.Context, p *PeerRecord, current ed25519.PublicKey, r reply, state string, err error) {
	switch {
	case err != nil:
		e.log.Info("msfp: a peer couldn't be told this server's new key", "peer", p.ID, "error", err)
	case r.status == http.StatusOK && state == wire.StateRotated:
		e.peerMoved(ctx, p.ID, current)
	case r.status == http.StatusAccepted:
		e.rotationAnswer(ctx, p.ID, current, wire.StatePending)
	case r.status == http.StatusConflict:
		e.rotationAnswer(ctx, p.ID, current, wire.RotationRefused)
	default:
		e.log.Info("msfp: a peer answered this server's new key", "peer", p.ID, "status", r.status, "problem", r.problem)
	}
}

// rotationAnswer keeps a peer's answer to our rotate (pending or
// refused), if it is still on our old key and current still our key.
func (e *Engine) rotationAnswer(ctx context.Context, id string, current ed25519.PublicKey, state string) {
	var ev []Event
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		ev = nil
		r, err := tx.Server()
		if err != nil || r == nil {
			return cmp.Or[error](err, ErrInternal)
		}
		if cur, err := e.openKey(r.Current, sealCurrent); err != nil || !wire.SameKey(cur.Public(), current) {
			return err
		}
		q, err := tx.Peer(id)
		if err != nil || q == nil || q.OurKey != KeyOld || q.RotationAnswer == state {
			return err
		}
		q.RotationAnswer = state
		ev = []Event{RotationAnswered{Peer: q.ID, Name: q.Name, State: state}}
		return tx.PutPeer(*q)
	})
	if err != nil {
		e.log.Warn("msfp: a peer's answer to this server's new key couldn't be kept", "peer", id, "error", err)
		return
	}
	e.notify(ev...)
}

// peerMoved is peer id knowing us by current now. It is kept only if
// current is still our current key during a change: a move learnt late,
// after the next change began, was to the key that is old now, and
// changes nothing. The last peer to move ends the change, in the same
// Update. moved says whether it was kept.
func (e *Engine) peerMoved(ctx context.Context, id string, current ed25519.PublicKey) (moved bool) {
	var events []Event
	err := e.changeServer(ctx, func(tx Tx, r *ServerRecord) error {
		events, moved = nil, false
		if r.Old == nil {
			return errUnchanged
		}
		cur, err := e.openKey(r.Current, sealCurrent)
		if err != nil {
			return err
		}
		if !wire.SameKey(cur.Public(), current) {
			return errUnchanged
		}
		q, err := tx.Peer(id)
		if err != nil {
			return err
		}
		if q == nil || q.OurKey != KeyOld {
			return errUnchanged
		}
		q.OurKey, q.RotationAnswer, q.CommitmentTold = KeyCurrent, "", true
		if err := tx.PutPeer(*q); err != nil {
			return err
		}
		moved = true
		events = []Event{RotationAnswered{Peer: q.ID, Name: q.Name, State: wire.StateRotated}}
		peers, err := tx.Peers()
		if err != nil {
			return err
		}
		for _, p := range peers {
			if p.OurKey == KeyOld {
				return nil
			}
		}
		ev, err := e.endRotation(tx, r)
		events = append(events, ev...)
		return err
	})
	if err != nil {
		e.log.Warn("msfp: a peer's move to this server's new key couldn't be kept", "peer", id, "error", err)
		return false
	}
	if moved {
		e.log.Info("msfp: a peer moved to this server's new key", "peer", id)
	}
	e.tellEnded(events)
	return moved
}

// finishRotation ends the change of key away from old: the old key
// deleted, the peers that didn't move lost. Only that change: one that
// ended meanwhile, or the next one begun since, is left as it is (ended
// false).
func (e *Engine) finishRotation(ctx context.Context, old ed25519.PublicKey) (ended bool, err error) {
	var events []Event
	err = e.changeServer(ctx, func(tx Tx, r *ServerRecord) error {
		events, ended = nil, false
		if r.Old == nil {
			return errUnchanged
		}
		kept, err := e.openKey(r.Old, sealOld)
		if err != nil {
			return err
		}
		if !wire.SameKey(kept.Public(), old) {
			return errUnchanged
		}
		events, err = e.endRotation(tx, r)
		ended = err == nil
		return err
	})
	if err != nil {
		return false, err
	}
	e.tellEnded(events)
	return ended, nil
}

// endRotation deletes the old key in r, and marks lost, in tx, the peers
// still on it: they must pair again (MSFP core section 12.4).
func (e *Engine) endRotation(tx Tx, r *ServerRecord) ([]Event, error) {
	r.Old, r.OldSince, r.OldRevoked = nil, time.Time{}, false
	peers, err := tx.Peers()
	if err != nil {
		return nil, err
	}
	var events []Event
	for _, p := range peers {
		if p.OurKey != KeyOld {
			continue
		}
		p.OurKey, p.RotationAnswer = KeyLost, ""
		if err := tx.PutPeer(p); err != nil {
			return nil, err
		}
		events = append(events, OurKeyLost{Peer: p.ID, Name: p.Name})
	}
	return append(events, RotationEnded{Lost: len(events)}), nil
}

// tellEnded logs the end of a change of key among events, once
// committed, then tells them.
func (e *Engine) tellEnded(events []Event) {
	for _, ev := range events {
		if end, ok := ev.(RotationEnded); ok && end.Lost > 0 {
			e.log.Warn("msfp: this server's old key deleted; peers that didn't move to the new one must pair again", "peers", end.Lost)
		} else if ok {
			e.log.Info("msfp: every peer moved to this server's new key; the old one deleted")
		}
	}
	e.notify(events...)
}

// rotateAtContact is a contact with p, a peer on our old key: a rotate
// sent first, unless one went within the spacing. It gives p as it is
// then.
func (e *Engine) rotateAtContact(ctx context.Context, p *PeerRecord, background bool) *PeerRecord {
	spacing := rotateSpacing
	if p.RotationAnswer == wire.StatePending {
		spacing = rotatePendingSpacing
	}
	if e.now().Sub(p.RotateSent) < spacing {
		return p
	}
	if q := e.rotateIfDue(ctx, p.ID, spacing, background); q != nil {
		return q
	}
	return p
}

// rotateSoon is rotateAtContact in the background: p just called, still
// on our old key.
func (e *Engine) rotateSoon(p *PeerRecord) {
	spacing := rotateSpacing
	if p.RotationAnswer == wire.StatePending {
		spacing = rotatePendingSpacing
	}
	if e.now().Sub(p.RotateSent) < spacing {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*callTimeout)
		defer cancel()
		e.rotateIfDue(ctx, p.ID, spacing, true)
	}()
}

// helloUnderNew is p not hearing our old key (an unsigned refusal): a
// hello signed with the current key, whose signed answer, whatever it
// says, means p moved (MSFP core section 12.4).
func (e *Engine) helloUnderNew(ctx context.Context, p *PeerRecord) (*PeerRecord, bool) {
	current := e.current()
	var raw json.RawMessage
	if _, err := e.exchange(ctx, p, callHello, e.ourHello(ctx, viewOf(p), p, false), &raw, current); err != nil {
		return p, false
	}
	if !e.peerMoved(ctx, p.ID, current.Public()) {
		return p, false
	}
	q, err := e.peerRecord(ctx, p.ID)
	if err != nil {
		return p, false
	}
	return q, true
}

// maintainKeys is what time does to keys, in RunOnce (MSFP core sections
// 12.4, 12.6, 12.7): our old key deleted 30 days after the change began
// (that change only), the key revoked with no next key 30 days after; a
// peer suspended 30 days without a valid rotation gone; a peer on our
// old key told again daily.
func (e *Engine) maintainKeys(ctx context.Context) error {
	now := e.now()
	k := e.ourKeys()
	if k.old != nil && now.Sub(k.oldSince) >= keyOverlap {
		if _, err := e.finishRotation(ctx, k.old.Public()); err != nil {
			return err
		}
	}
	if k.revoked != nil && now.Sub(k.revokedAt) >= keyOverlap {
		err := e.changeServer(ctx, func(_ Tx, r *ServerRecord) error {
			if r.Revoked == nil || !r.RevokedAt.Equal(k.revokedAt) {
				return errUnchanged
			}
			r.Revoked, r.RevokedAt = nil, time.Time{}
			return nil
		})
		if err != nil {
			return err
		}
	}
	var events []Event
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		events = nil
		peers, err := tx.Peers()
		if err != nil {
			return err
		}
		for _, p := range peers {
			if p.Suspended.IsZero() || p.State == Gone || now.Sub(p.Suspended) < keyOverlap {
				continue
			}
			// Gone, and still suspended: a paired hello doesn't bring it
			// back (MSFP core section 12.6).
			p.State = Gone
			if err := tx.PutPeer(p); err != nil {
				return err
			}
			e.log.Warn("msfp: a peer whose key was revoked 30 days ago, with no new key since, is gone", "peer", p.ID, "name", p.Name)
			events = append(events, PeerGone{Peer: p.ID, Name: p.Name})
		}
		return nil
	})
	if err != nil {
		return err
	}
	e.notify(events...)
	e.eachPeer(ctx, func(p *PeerRecord) bool {
		return p.OurKey == KeyOld && p.Inbound == InboundYes && p.Suspended.IsZero()
	}, func(ctx context.Context, p *PeerRecord) { e.rotateIfDue(ctx, p.ID, rotateDaily, true) })
	return nil
}
