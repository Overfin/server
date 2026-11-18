package federation

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/overfin/server/internal/msfp"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
)

// Changes of key (MSFP core section 12), this server's and its peers'.
//
// This server keeps a next key ready, made in advance; its peers hold the
// commitment to it (SHA-256 of its public key), from pair, from a
// rotation, or, for a pairing that had none, from the first hello that
// carried it. When our operator changes the key, the next key becomes the
// current one: every peer is sent a rotate, signed with the old key, and
// moves to the new one at once since it matches the commitment. Until a
// peer answered that it did, this server signs toward it with the old key,
// and tells it again at each contact and at least daily; after 30 days,
// the old key is deleted, and a peer that didn't move must pair again.
//
// A key that was stolen is revoked: every peer suspends it, and hears
// nothing from it but a rotation to the committed key, which lifts the
// suspension; after 30 days without one, the peer is gone.

// Paths of the calls of changes of key.
const (
	rotatePath = "/api/v1/federation/rotate"
	revokePath = "/api/v1/federation/revoke"
)

var (
	// ErrSuspended: the peer revoked its key; no call is made to it
	// (MSFP core section 12.6).
	ErrSuspended = errors.New("federation: the other server's key was revoked")
	// ErrKeyLost: the peer didn't move to this server's new key within 30
	// days, and no longer knows any key of ours: pair again.
	ErrKeyLost = errors.New("federation: the other server didn't follow this server's change of key")
	// ErrNoPending: no rotation of that peer waits for our operator.
	ErrNoPending = errors.New("federation: no change of key to confirm")
	// ErrKeyTaken: the key a peer moves to is another peer's, or a
	// forgotten one's.
	ErrKeyTaken = errors.New("federation: that key belongs to another server")
	// errRefused: a rotation refused (409 rotation-refused).
	errRefused = errors.New("federation: rotation refused")
)

func ptr[T any](v T) *T { return &v }

// heardKey says whether key, which found p, is still heard: its current
// key, or the one it rotated away from, for PreviousHeard.
func heardKey(p *store.Peer, key ed25519.PublicKey, now time.Time) bool {
	if msfp.SameKey(key, p.PublicKey) {
		return true
	}
	return msfp.SameKey(key, p.PreviousKey) && now.Before(p.PreviousUntil)
}

// signer is the key this server signs with toward p: the old one while p
// hasn't moved from it (MSFP core section 12.4), the current one
// otherwise.
func (s *Service) signer(p *store.Peer) msfp.Key {
	if p.OurKey == store.OurKeyOld {
		if old, _, ok := s.Keys.Old(); ok {
			return old
		}
	}
	return s.Keys.Current()
}

// A peer's change of key, here.

// rotate is a peer telling it changed its key (MSFP core sections 9.3 and
// 12.3).
func (s *Service) rotate(w http.ResponseWriter, r *http.Request) {
	v, body, ok := s.verified(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	p, err := s.Store.PeerByKey(ctx, v.Key)
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	rot, err := msfp.ReadRotate(body, v.Key, s.now())
	if err != nil {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	state, err := s.receiveRotation(ctx, p, rot)
	switch {
	case errors.Is(err, errRefused):
		problem.Send(w, problem.RotationRefused, "")
	case err != nil:
		s.Logger.Error("federation: a peer's change of key couldn't be kept", "name", p.Name, "error", err)
		problem.Send(w, problem.Internal, "")
	case state == msfp.StatePending:
		s.answerState(w, v.Nonce, http.StatusAccepted, state)
	default:
		s.answerState(w, v.Nonce, http.StatusOK, state)
	}
}

// answerState answers {"state": state}, signed.
func (s *Service) answerState(w http.ResponseWriter, nonce string, status int, state string) {
	body, err := msfp.Marshal(map[string]string{"state": state})
	if err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	s.answerWith(w, nonce, status, body)
}

// receiveRotation handles a rotation from p, checked (msfp.ReadRotate), as
// MSFP core section 12.3 says: rotated (its new key current now), pending
// (for our operator to confirm), or errRefused.
func (s *Service) receiveRotation(ctx context.Context, p *store.Peer, rot msfp.Rotation) (string, error) {
	if !msfp.SameKey(rot.Old, p.PublicKey) {
		// Signed with the key it moved from, still heard: a retry of the
		// rotation that took effect changes nothing; any other is refused.
		if msfp.SameKey(rot.New, p.PublicKey) {
			return msfp.StateRotated, nil
		}
		return "", errRefused
	}
	// The new key may not be another peer's, nor a forgotten one.
	switch other, err := s.Store.PeerByKey(ctx, rot.New); {
	case err == nil && other.ID != p.ID:
		return "", errRefused
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return "", err
	}
	if forgotten, err := s.Store.PeerForgotten(ctx, rot.New); err != nil || forgotten {
		if err == nil {
			err = errRefused
		}
		return "", err
	}
	now := s.now()
	match := p.NextCommitment != nil && bytes.Equal(msfp.Commitment(rot.New), p.NextCommitment)
	switch {
	case match:
		// The key it committed to: at once, a suspension lifted.
		if err := s.Store.RotatePeer(ctx, p.ID, rot, now.Add(PreviousHeard), now); err != nil {
			return "", err
		}
		if p.Suspended != nil {
			s.Logger.Warn("federation: a peer whose key was revoked moved to the key it had committed to: no longer suspended",
				"name", p.Name, "fingerprint", msfp.Fingerprint(rot.New))
			s.changed(p.ID)
		} else {
			s.Logger.Info("federation: a peer changed its key, to the one it had committed to", "name", p.Name,
				"from", msfp.Fingerprint(rot.Old), "to", msfp.Fingerprint(rot.New))
		}
		return msfp.StateRotated, nil
	case p.NextCommitment != nil || p.Suspended != nil:
		// Someone holding its key tried to move the pairing to another
		// key: refused, and shown to our operator.
		if err := s.Store.SetPeerRefused(ctx, p.ID, rot.New, now); err != nil {
			return "", err
		}
		s.Logger.Warn("federation: a peer's change of key refused: the new key isn't the one it committed to",
			"name", p.Name, "fingerprint", msfp.Fingerprint(rot.New))
		return "", errRefused
	}
	// No commitment: our operator confirms the new key's fingerprint
	// with the other operator. Repeated, the newest statement is kept;
	// another key replaces it.
	if p.Pending == nil || !msfp.SameKey(p.Pending.New, rot.New) {
		s.Logger.Info("federation: a peer changed its key, waiting for the operator to confirm it", "name", p.Name,
			"fingerprint", msfp.Fingerprint(rot.New))
	}
	if err := s.Store.SetPeerPending(ctx, p.ID, &rot); err != nil {
		return "", err
	}
	return msfp.StatePending, nil
}

// revoke is a peer saying its key was stolen (MSFP core sections 9.4 and
// 12.6): suspended.
func (s *Service) revoke(w http.ResponseWriter, r *http.Request) {
	v, body, ok := s.verified(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	p, err := s.Store.PeerByKey(ctx, v.Key)
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	if _, err := msfp.ReadRevoke(body, v.Key, s.now()); err != nil {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	if !msfp.SameKey(v.Key, p.PublicKey) {
		// The key it moved from, still heard for a while: no longer, at
		// once. Its current key isn't concerned.
		if err := s.Store.DropPreviousKey(ctx, p.ID); err != nil {
			problem.Send(w, problem.Internal, "")
			return
		}
		s.answerState(w, v.Nonce, http.StatusOK, msfp.StateSuspended)
		return
	}
	suspended, err := s.Store.SuspendPeer(ctx, p.ID, s.now())
	if err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	if suspended {
		s.Logger.Warn("federation: a peer revoked its key: suspended until it moves to the key it committed to",
			"name", p.Name, "fingerprint", msfp.Fingerprint(p.PublicKey))
		s.changed(p.ID)
	}
	s.answerState(w, v.Nonce, http.StatusOK, msfp.StateSuspended)
}

// ConfirmRotation is our operator confirming a peer's new key, having
// compared its fingerprint with the other operator (MSFP core section
// 12.3, case 4): it becomes the peer's key.
func (s *Service) ConfirmRotation(ctx context.Context, id string) (*store.Peer, error) {
	p, err := s.Store.PeerByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Suspended != nil {
		return nil, ErrSuspended
	}
	if p.Pending == nil || !msfp.SameKey(p.Pending.Old, p.PublicKey) {
		return nil, ErrNoPending
	}
	rot := *p.Pending
	switch other, err := s.Store.PeerByKey(ctx, rot.New); {
	case err == nil && other.ID != p.ID:
		return nil, ErrKeyTaken
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return nil, err
	}
	if forgotten, err := s.Store.PeerForgotten(ctx, rot.New); err != nil || forgotten {
		if err == nil {
			err = ErrKeyTaken
		}
		return nil, err
	}
	now := s.now()
	if err := s.Store.RotatePeer(ctx, p.ID, rot, now.Add(PreviousHeard), now); err != nil {
		return nil, err
	}
	s.Logger.Info("federation: a peer's new key confirmed by the operator", "name", p.Name, "fingerprint", msfp.Fingerprint(rot.New))
	q, err := s.Store.PeerByID(ctx, id)
	if err != nil {
		return nil, err
	}
	// It learns at our next contact, when it tells us again: one now.
	go func() {
		ctx, cancel := context.WithTimeout(Background(context.Background()), 20*time.Second)
		defer cancel()
		s.Check(ctx, q)
	}()
	return q, nil
}

// DismissRefused is our operator having seen a refused change of a peer's
// key.
func (s *Service) DismissRefused(ctx context.Context, id string) error {
	if _, err := s.Store.PeerByID(ctx, id); err != nil {
		return err
	}
	return s.Store.SetPeerRefused(ctx, id, nil, time.Time{})
}

// This server's change of key.

// rotateSoon tells p of our new key again, in the background: it just
// called, under our old key.
func (s *Service) rotateSoon(p *store.Peer) {
	go func() {
		ctx, cancel := context.WithTimeout(Background(context.Background()), 20*time.Second)
		defer cancel()
		s.rotateAtContact(ctx, p)
	}()
}

// The least time between two rotates sent to a peer at contacts: each call
// doesn't need its own. A peer whose operator was to confirm our new key
// is told sooner: its call may be the first since its operator confirmed,
// and it checks our answers against the new key only, so until it's told
// again, its calls fail.
const (
	rotateAtSpacing        = time.Minute
	rotatePendingAtSpacing = 10 * time.Second
)

// rotateAtContact sends p a rotate unless one went within the spacing,
// and returns p as it is then. Of contacts at once, one only sends it:
// the store gives the right to (ClaimRotate).
func (s *Service) rotateAtContact(ctx context.Context, p *store.Peer) *store.Peer {
	spacing := rotateAtSpacing
	if p.RotationAnswer == msfp.StatePending {
		spacing = rotatePendingAtSpacing
	}
	now := s.now()
	if p.RotateSent != nil && now.Sub(*p.RotateSent) < spacing {
		return p
	}
	claimed, err := s.Store.ClaimRotate(ctx, p.ID, now, now.Add(-spacing))
	if err != nil {
		s.Logger.Error("federation: a rotate couldn't be noted", "error", err)
		return p
	}
	if !claimed {
		return p
	}
	s.rotateNoted(ctx, p, now)
	if q, err := s.Store.PeerByID(ctx, p.ID); err == nil {
		return q
	}
	return p
}

// sendRotate tells p our key changed (MSFP core section 12.4): a rotate,
// newly dated, signed with the old key.
func (s *Service) sendRotate(ctx context.Context, p *store.Peer) {
	if _, _, ok := s.Keys.Old(); !ok || p.OurKey != store.OurKeyOld {
		return
	}
	now := s.now()
	if err := s.Store.SetRotateSent(ctx, p.ID, now); err != nil {
		s.Logger.Error("federation: a rotate couldn't be noted", "error", err)
		return
	}
	s.rotateNoted(ctx, p, now)
}

// rotateNoted sends p the rotate noted as sent at now (sendRotate).
func (s *Service) rotateNoted(ctx context.Context, p *store.Peer, now time.Time) {
	old, _, ok := s.Keys.Old()
	if !ok {
		return
	}
	current := s.Keys.Current()
	rot := msfp.SignRotation(old, current, s.Keys.NextCommitment(), now)
	var answer struct {
		State string `json:"state"`
	}
	r, err := s.call(ctx, p, rotatePath, rot.Body(), &answer)
	// A hello under the new key may have found it moved already.
	if q, err := s.Store.PeerByID(ctx, p.ID); err != nil || q.OurKey != store.OurKeyOld {
		return
	}
	switch {
	case err != nil:
		s.Logger.Info("federation: a peer couldn't be told of this server's new key", "name", p.Name, "error", err)
	case r.Status == http.StatusOK && answer.State == msfp.StateRotated:
		s.peerMoved(ctx, p, current.Public())
	case r.Status == http.StatusAccepted:
		s.Store.SetOurKey(ctx, p.ID, store.OurKeyOld, msfp.StatePending)
		s.Logger.Info("federation: a peer's operator has to confirm this server's new key", "name", p.Name)
	case r.Status == http.StatusConflict:
		s.Store.SetOurKey(ctx, p.ID, store.OurKeyOld, "refused")
		s.Logger.Warn("federation: a peer refused this server's new key", "name", p.Name)
	default:
		s.Logger.Info("federation: a peer answered this server's new key", "name", p.Name, "status", r.Status, "problem", r.Problem)
	}
}

// helloUnderNew is p not hearing our old key (an unsigned 404): a hello
// signed with the new key, whose signed answer, whatever it says, means p
// moved (MSFP core section 12.4).
func (s *Service) helloUnderNew(ctx context.Context, p *store.Peer) (*store.Peer, bool) {
	var raw json.RawMessage
	current := s.Keys.Current()
	if _, err := s.callOnce(ctx, p, helloPath, s.ourHello(ctx, p, viewOf(p), false), &raw, current); err != nil {
		return p, false
	}
	s.peerMoved(ctx, p, current.Public())
	q, err := s.Store.PeerByID(ctx, p.ID)
	if err != nil {
		return p, false
	}
	return q, true
}

// peerMoved is p knowing us by current, our new key, now; the last one
// to move ends the change, the old key deleted. The store notes it only
// if current is still our key: a move seen late, after the next change
// of key began, was to the key that is old now, and changes nothing.
// Called with or without rotations held: it doesn't take it.
func (s *Service) peerMoved(ctx context.Context, p *store.Peer, current ed25519.PublicKey) {
	moved, ended, err := s.Store.PeerMovedTo(ctx, p.ID, current)
	if err != nil {
		s.Logger.Error("federation: a peer's move to this server's new key couldn't be kept", "error", err)
		return
	}
	if !moved {
		return
	}
	s.Logger.Info("federation: a peer moved to this server's new key", "name", p.Name)
	if !ended {
		return
	}
	if err := s.Keys.reload(ctx, s.Store); err != nil {
		s.Logger.Error("federation: this server's keys couldn't be read again", "error", err)
		return
	}
	s.Logger.Info("federation: every peer moved to this server's new key; the old one deleted")
}

// finishRotation ends the change of key away from old: the old key
// deleted, peers that didn't move told to pair again (in the panel).
// Only that change: one that ended meanwhile, and the next one begun
// since, are left as they are (Store.FinishFederationRotation).
func (s *Service) finishRotation(ctx context.Context, old ed25519.PublicKey) error {
	ended, n, err := s.Store.FinishFederationRotation(ctx, old)
	if err != nil {
		return err
	}
	if err := s.Keys.reload(ctx, s.Store); err != nil {
		return err
	}
	switch {
	case !ended:
	case n > 0:
		s.Logger.Warn("federation: this server's old key deleted; servers that didn't move to the new one must pair again", "servers", n)
	default:
		s.Logger.Info("federation: every peer moved to this server's new key; the old one deleted")
	}
	return nil
}

// nextKey is the next key, to rotate to: given back as exported (text),
// or its seed on the server when text is empty. ErrNotNextKey for a text
// that isn't the committed key, ErrNoNextKey when its seed isn't here and
// none is given.
func (s *Service) nextKey(ctx context.Context, text string) (msfp.Key, error) {
	if strings.TrimSpace(text) != "" {
		k, err := parseNextKey(text)
		if err != nil || !bytes.Equal(msfp.Commitment(k.Public()), s.Keys.NextCommitment()) {
			return msfp.Key{}, ErrNotNextKey
		}
		return k, nil
	}
	stored, err := s.Store.FederationKeys(ctx)
	if err != nil {
		return msfp.Key{}, err
	}
	if stored.Next == nil {
		return msfp.Key{}, ErrNoNextKey
	}
	return msfp.KeyFromSeed(stored.Next)
}

// rotations makes one change of key at a time.
var rotations sync.Mutex

// Rotate is our operator changing this server's key, to the next key as
// committed: its seed on the server, or given back as exported (nextKey).
// Every peer is told at once. A change still under way is finished
// first: peers that hadn't moved must pair again.
func (s *Service) Rotate(ctx context.Context, nextKey string) error {
	rotations.Lock()
	defer rotations.Unlock()
	next, err := s.nextKey(ctx, nextKey)
	if err != nil {
		return err
	}
	return s.rotateTo(ctx, next, false)
}

// rotateTo moves this server to next; revoked says its current key was
// just revoked.
func (s *Service) rotateTo(ctx context.Context, next msfp.Key, revoked bool) error {
	if old, _, ok := s.Keys.Old(); ok {
		if err := s.finishRotation(ctx, old.Public()); err != nil {
			return err
		}
	}
	old := s.Keys.Current()
	after := msfp.NewKey()
	if err := s.Store.RotateFederationKey(ctx, next.Seed(), after.Seed(), msfp.Commitment(after.Public()), s.now(), revoked); err != nil {
		return err
	}
	if err := s.Keys.reload(ctx, s.Store); err != nil {
		return err
	}
	s.Logger.Info("federation: this server's key changed", "from", msfp.Fingerprint(old.Public()), "to", msfp.Fingerprint(next.Public()))
	// A peer that can't be called learns it in our answers to its next
	// hello (MSFP core section 12.4).
	s.each(ctx, func(p *store.Peer) bool { return p.OurKey == store.OurKeyOld && p.Callable() }, s.sendRotate)
	return nil
}

// each runs do for every peer keep says, all at once, until all are done.
func (s *Service) each(ctx context.Context, keep func(*store.Peer) bool, do func(context.Context, *store.Peer)) {
	peers, err := s.Store.Peers(ctx)
	if err != nil {
		s.Logger.Error("federation: listing peers", "error", err)
		return
	}
	var wg sync.WaitGroup
	for _, p := range peers {
		if keep(p) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				do(ctx, p)
			}()
		}
	}
	wg.Wait()
}

// Revoke is our operator saying this server's key was stolen (MSFP core
// section 12.7): every peer is told the key is revoked, and suspends it.
// Then, with the next key (its seed on the server, or given back as
// exported), the server moves to it at once, and peers holding its
// commitment follow (rotated true). Without it (ErrNoNextKey from
// nextKey: none here, none given), or withoutNext (the next key may be
// stolen too), the server gets a new key and every pairing ends: each is
// made again with a new code.
func (s *Service) Revoke(ctx context.Context, nextKey string, withoutNext bool) (rotated bool, err error) {
	rotations.Lock()
	defer rotations.Unlock()
	var next msfp.Key
	haveNext := false
	if !withoutNext {
		next, err = s.nextKey(ctx, nextKey)
		if err != nil && !errors.Is(err, ErrNoNextKey) {
			return false, err
		}
		haveNext = err == nil
	}
	// A change under way is finished first: every peer left then knows
	// us by the key revoked.
	if old, _, ok := s.Keys.Old(); ok {
		if err := s.finishRotation(ctx, old.Public()); err != nil {
			return false, err
		}
	}
	key := s.Keys.Current()
	statement := msfp.SignRevocation(key, s.now())
	s.Logger.Warn("federation: this server's key revoked", "fingerprint", msfp.Fingerprint(key.Public()))
	// A peer that can't be called learns it in our answers to its next
	// hello, signed with the key revoked, for 30 days (MSFP core section
	// 12.6): only when this server moves to its next key, which keeps the
	// revoked one as its old key meanwhile.
	s.each(ctx, func(p *store.Peer) bool { return p.OurKey == store.OurKeyCurrent && p.Callable() }, func(ctx context.Context, p *store.Peer) {
		var answer json.RawMessage
		if r, err := s.call(ctx, p, revokePath, statement.Body(), &answer); err != nil || r.Status != http.StatusOK {
			s.Logger.Info("federation: a peer couldn't be told this server's key is revoked", "name", p.Name, "status", r.Status, "error", err)
		}
	})
	if haveNext {
		return true, s.rotateTo(ctx, next, true)
	}
	peers, err := s.Store.Peers(ctx)
	if err != nil {
		return false, err
	}
	fresh, after := msfp.NewKey(), msfp.NewKey()
	if err := s.Store.ReplaceFederationKey(ctx, fresh.Seed(), after.Seed(), msfp.Commitment(after.Public())); err != nil {
		return false, err
	}
	if err := s.Keys.reload(ctx, s.Store); err != nil {
		return false, err
	}
	for _, p := range peers {
		s.changed(p.ID)
	}
	s.Logger.Warn("federation: this server has a new key, without its next one: every pairing ended, to be made again",
		"fingerprint", msfp.Fingerprint(fresh.Public()), "servers", len(peers))
	return false, nil
}

// NextKey is the next key as the panel shows it: its fingerprint (from
// its commitment), and whether its seed is on the server.
func (s *Service) NextKey(ctx context.Context) (fingerprint string, onServer bool, err error) {
	stored, err := s.Store.FederationKeys(ctx)
	if err != nil {
		return "", false, err
	}
	return msfp.CommitmentFingerprint(s.Keys.NextCommitment()), stored.Next != nil, nil
}

// ExportNextKey is the next key's seed as text, for our operator to keep
// offline, with its fingerprint to check it by. ErrNoNextKey once deleted
// from the server.
func (s *Service) ExportNextKey(ctx context.Context) (text, fingerprint string, err error) {
	stored, err := s.Store.FederationKeys(ctx)
	if err != nil {
		return "", "", err
	}
	if stored.Next == nil {
		return "", "", ErrNoNextKey
	}
	return NextKeyText(stored.Next), msfp.CommitmentFingerprint(s.Keys.NextCommitment()), nil
}

// DeleteNextKey deletes the next key's seed from the server, once our
// operator keeps it offline: someone who copies the server's storage
// then can't change its key (MSFP core section 12.5).
func (s *Service) DeleteNextKey(ctx context.Context) error {
	if err := s.Store.DeleteFederationNextSeed(ctx); err != nil {
		return err
	}
	s.Logger.Info("federation: the next key's seed deleted from this server, kept offline by the operator")
	return nil
}

// rotateDaily is how often, at least, a peer on our old key is told again
// (MSFP core section 12.4).
const rotateDaily = 24 * time.Hour

// Maintain does what time brings to keys and addresses, run every hour
// or so (MSFP core sections 12 and 13; maintainAddresses for addresses):
// our old key deleted 30 days after the change began;
// a peer on it told again daily; a peer suspended 30 days without a
// valid rotation becomes gone; and a peer that holds no commitment of
// ours, from a pairing made before commitments, sent a hello carrying it
// once.
func (s *Service) Maintain(ctx context.Context) {
	now := s.now()
	// That change only: one our operator made meanwhile isn't ended.
	if old, since, ok := s.Keys.Old(); ok && now.Sub(since) >= KeyOverlap {
		rotations.Lock()
		err := s.finishRotation(ctx, old.Public())
		rotations.Unlock()
		if err != nil {
			s.Logger.Error("federation: the old key couldn't be deleted", "error", err)
		}
	}
	peers, err := s.Store.Peers(ctx)
	if err != nil {
		s.Logger.Error("federation: listing peers", "error", err)
		return
	}
	s.maintainAddresses(ctx, peers)
	for _, p := range peers {
		switch {
		case p.Suspended != nil && p.State != store.PeerGone && now.Sub(*p.Suspended) >= KeyOverlap:
			if _, err := s.Store.SetPeerState(ctx, p.ID, p.State, store.PeerGone); err != nil {
				s.Logger.Error("federation: a suspended peer couldn't be marked gone", "error", err)
				continue
			}
			s.changed(p.ID)
			s.Logger.Warn("federation: a peer whose key was revoked 30 days ago, with no new key since, is gone: remove it, and pair again if you wish",
				"name", p.Name)
		case !p.Callable():
			// Told in our answers to its hellos (MSFP core section 7.5).
		case p.OurKey == store.OurKeyOld && (p.RotateSent == nil || now.Sub(*p.RotateSent) >= rotateDaily):
			s.sendRotate(ctx, p)
		case !p.CommitmentTold && p.Usable() && s.firstTell(p.ID):
			s.Check(ctx, p)
		}
	}
}

// firstTell says, once per peer for a Service, that it's yet to be sent
// our commitment in a hello.
func (s *Service) firstTell(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.told == nil {
		s.told = map[string]bool{}
	}
	if s.told[id] {
		return false
	}
	s.told[id] = true
	return true
}
