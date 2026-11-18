package federation

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/overfin/server/internal/outbound"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/msfp/wire"
)

// Servers that can't be called (MSFP core section 7.5), this one and its
// peers.
//
// This server can't be called when its operator says so (Uncallable): at
// home behind a router, say, it can connect out, but nothing can connect
// to it. Its address is then https://msfp-<26 characters>.invalid, which
// never resolves, made once and kept; it makes no codes, but redeems
// them; it says so in every pair and hello; and it asks each peer, on a
// schedule (Hellos), what it would otherwise be told: that its operator
// accepted, the peer's changes of key (pulled from its answers:
// applyPulled), its changes of address. It can watch what its peers
// share; it shares nothing.
//
// A peer that says it can't be called is never connected to: no hello,
// no rotate or revoke, no catalog read, no play, no media (call refuses
// them all), and what it held for us is dropped. Its hellos are answered
// with our changes of key (pullable), and the key it knows us by is read
// from them (readKnownKey). A peer that says it can be called again is
// called only once a hello at its address is answered under its key.

var (
	// ErrUncallable: the peer can't be called; no connection is made to
	// it (MSFP core section 7.5).
	ErrUncallable = errors.New("federation: the other server can't be called")
	// ErrNoCodes: this server can't be called, and makes no codes: the
	// other operator makes one.
	ErrNoCodes = errors.New("federation: a server that can't be called makes no pairing codes")
	// errNoConnection: a call that couldn't connect (any failure of the
	// connection but a certificate refused), as callsFailed says.
	errNoConnection = errors.New("federation: couldn't connect")
)

// certificateRefused says whether a connection failed on the peer's
// certificate, or on our own refusal of its address, rather than to
// connect.
func certificateRefused(err error) bool {
	return errors.Is(err, wire.ErrUntrustedCertificate) || errors.Is(err, outbound.ErrRefused)
}

// knownKey is what this server's hello requests to p say, when it can't be
// called (MSFP core section 12.4): the key it checks p's answers against,
// and "pending" or "refused" while p's change of key waits for our
// operator, or was refused.
func (s *Service) knownKey(p *store.Peer) (key string, rotation any) {
	key = wire.Encode64(p.PublicKey)
	switch {
	case p.Pending != nil && wire.SameKey(p.Pending.Old, p.PublicKey):
		return key, wire.StatePending
	case p.RefusedKey != nil:
		return key, wire.RotationRefused
	}
	return key, nil
}

// readKnownKey reads, from the hello of p, a server that can't be called,
// which of our keys it knows us by (MSFP core section 12.4, step 4): while
// our key changes, the new one means p moved, and this answer is signed
// with it already; the old one, that it hasn't, and why when it says. A key
// that is neither of ours is ignored.
func (s *Service) readKnownKey(ctx context.Context, w http.ResponseWriter, nonce string, p *store.Peer, h wire.Hello) {
	if p.OurKey != store.OurKeyOld || h.KnownKey == nil {
		return
	}
	old, _, ok := s.Keys.Old()
	switch {
	case wire.SameKey(h.KnownKey, s.Keys.Current().Public()):
		s.peerMoved(ctx, p, h.KnownKey)
		s.signsFor(w, nonce, s.Keys.Current())
	case ok && wire.SameKey(h.KnownKey, old.Public()) && h.RotationState != "" && h.RotationState != p.RotationAnswer:
		if err := s.Store.SetOurKey(ctx, p.ID, store.OurKeyOld, h.RotationState); err != nil {
			s.Logger.Error("federation: a peer's answer to this server's new key couldn't be kept", "error", err)
		}
	}
}

// pullable adds to an answer to p, a server that can't be called, what
// it can't be sent otherwise (MSFP core sections 12.4 and 12.6): while p
// knows us by our old key, our rotation statement, newly dated; and when
// that old key was revoked, the revocation, newly dated too. Both travel
// in answers signed with the old key only.
func (s *Service) pullable(p *store.Peer, h *helloMessage) {
	old, _, ok := s.Keys.Old()
	if !ok || p.OurKey != store.OurKeyOld {
		return
	}
	now := s.now()
	h.Rotation = wire.SignRotation(old, s.Keys.Current(), s.Keys.NextCommitment(), now).Body()
	if s.Keys.OldRevoked() {
		body := wire.SignRevocation(old, now).Body()
		h.Revocation = &body
	}
}

// applyPulled is a hello answer of p's, signed: its revocation, then,
// when this server can't be called, its rotation, as revoke and rotate
// would be (MSFP core sections 12.6 and 12.3), checked against p's key
// now, the one the answer verified under. onlyRotation, for a suspended
// p: its rotation only. Once p's new key is current, the next hello goes
// at once, to say we know it (section 12.4, step 3).
//
// A revocation is applied whether or not this server can be called: a
// server whose key was stolen with no next key answers every former
// peer's hello with it for 30 days, so that a peer revoke didn't reach
// learns of it too (section 12.7, case 2). Signed by the key it revokes,
// it can only stop a pairing, never start or move one.
func (s *Service) applyPulled(ctx context.Context, p *store.Peer, h wire.Hello, onlyRotation bool) {
	pulled := h.Pull(p.PublicKey, s.now())
	if pulled.Revocation != nil && !onlyRotation && p.Suspended == nil {
		suspended, err := s.Store.SuspendPeer(ctx, p.ID, s.now())
		if err != nil {
			s.Logger.Error("federation: a peer's revoked key couldn't be kept", "error", err)
			return
		}
		if suspended {
			s.Logger.Warn("federation: a peer revoked its key: suspended until it moves to the key it committed to",
				"name", p.Name, "fingerprint", wire.Fingerprint(p.PublicKey))
			s.changed(p.ID)
		}
	}
	if !s.Uncallable || pulled.Rotation == nil {
		return
	}
	q, err := s.Store.PeerByID(ctx, p.ID)
	if err != nil {
		return
	}
	state, err := s.receiveRotation(ctx, q, *pulled.Rotation)
	switch {
	case errors.Is(err, errRefused):
	case err != nil:
		s.Logger.Error("federation: a peer's change of key couldn't be kept", "name", p.Name, "error", err)
	case state == wire.StateRotated:
		s.helloNow(p.ID)
	}
}

// keepInbound keeps whether p can be called, as its signed hello says
// (MSFP core section 7.5). Once it can't, it is never connected to, and
// what it held for us is dropped. Once it says it can again, it is
// connected to only after a hello at its address answers under its key
// (announcedAddress starts that check, confirmMove ends it).
func (s *Service) keepInbound(ctx context.Context, p *store.Peer, uncallable bool) error {
	switch {
	case uncallable && p.Inbound != store.InboundNo:
		if err := s.Store.SetPeerInbound(ctx, p.ID, store.InboundNo); err != nil {
			return err
		}
		p.Inbound, p.Move = store.InboundNo, nil
		s.lim().setUnreached(wire.Encode64(p.PublicKey), false)
		// Its titles are dropped, and nothing of it plays here any more:
		// our viewers' playbacks from it end (no media is fetched from it).
		s.changed(p.ID)
		s.Logger.Info("federation: a peer says it can't be called: it can watch what this server shares with it, and shares nothing",
			"name", p.Name)
	case !uncallable && p.Inbound == store.InboundNo:
		if err := s.Store.SetPeerInbound(ctx, p.ID, store.InboundConfirming); err != nil {
			return err
		}
		p.Inbound = store.InboundConfirming
		s.Logger.Info("federation: a peer says it can be called again; its address is checked first", "name", p.Name)
	}
	return nil
}

// Hellos sends this server's scheduled hellos, when it can't be called
// (MSFP core section 7.5): to every peer, suspended ones included, whose
// turn it is, then its next turn set by where the pairing stands then
// (wire.NextHello: 20 seconds while its operator hasn't accepted, for a
// day; 5 minutes once paired; an hour once gone). Each hello counts
// against the peer's limits as any call does, and isn't sent while the
// peer asked to wait or doesn't answer (Limits: the next turn then). The
// gateway runs it every second; it returns once the hellos it sent are
// answered.
func (s *Service) Hellos(ctx context.Context) {
	if !s.Uncallable {
		return
	}
	peers, err := s.Store.Peers(ctx)
	if err != nil {
		s.Logger.Error("federation: listing peers", "error", err)
		return
	}
	now := s.now()
	var wg sync.WaitGroup
	for _, p := range peers {
		if !s.helloDue(p.ID, now) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, cancel := context.WithTimeout(Background(ctx), 20*time.Second)
			defer cancel()
			q, err := s.Check(c, p)
			if q == nil || errors.Is(err, store.ErrNotFound) {
				q = p
			}
			s.helloDone(q)
		}()
	}
	wg.Wait()
}

// operatorWaiting says whether p's operator hasn't accepted yet: the
// schedule's quickest turns.
func operatorWaiting(p *store.Peer) bool { return p.State == store.PeerTheirs }

// helloDue says whether p's scheduled hello goes now (its first at once),
// and if so, notes it under way: its next turn is set once it's answered
// (helloDone).
func (s *Service) helloDue(id string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turns == nil {
		s.turns = map[string]time.Time{}
	}
	if next, ok := s.turns[id]; ok && now.Before(next) {
		return false
	}
	s.turns[id] = now.Add(time.Hour) // under way
	return true
}

// helloDone sets p's next turn, as the pairing stands after its hello;
// unless helloNow asked for one at once meanwhile.
func (s *Service) helloDone(p *store.Peer) {
	now := s.now()
	wait := wire.NextHello(operatorWaiting(p), p.State == store.PeerGone, now.Sub(p.Created))
	s.mu.Lock()
	defer s.mu.Unlock()
	if next, ok := s.turns[p.ID]; ok && next.IsZero() {
		return
	}
	s.turns[p.ID] = now.Add(wait)
}

// helloNow has p's next scheduled hello go at the next run of Hellos.
func (s *Service) helloNow(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turns == nil {
		s.turns = map[string]time.Time{}
	}
	s.turns[id] = time.Time{}
}
