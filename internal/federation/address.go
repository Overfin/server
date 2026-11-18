package federation

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/overfin/server/internal/msfp"
	"github.com/overfin/server/internal/store"
)

// Changes of address (MSFP core section 13), this server's and its
// peers'.
//
// This server's address is its site's (Service.Address). When the
// configured one changes, the next start notes it (NoteAddress): the one
// before becomes the former address, still accepted, for requests signed
// for its host, 30 days or until every peer signs for the new one. Once
// this server answers at its new address (answersAt), every peer is told
// at once in a hello, and again at each contact while it hasn't moved;
// until then, its hellos name the former.
//
// A peer whose signed hello names another address than the one stored
// keeps being called at the stored one until a hello at the new address,
// signed for its host, is answered under the peer's key: then the new
// address is stored, with the pins that answer names. Failing, the old
// one stays; it's tried again at most once an hour, and the operator is
// warned after a day.
//
// A peer that can't be called (MSFP core section 7.5) moves otherwise:
// nothing calls it, so the address its signed hello names is stored at
// once. One that says it can be called again is confirmed as a move is,
// at its new address or, when it names none, at the one stored, before
// anything else calls it.
//
// This server, turning "can't be called" on, moves to its address of the
// form https://msfp-<26 characters>.invalid: nothing calls it there, so
// no former address is kept, and every peer is told at once. Turning it
// off, it moves back to its site's address, as any move.

// moveLate is when the panel warns that a peer hasn't moved to this
// server's new address (core section 13.1: "after some days").
const moveLate = 3 * 24 * time.Hour

// announceSpacing is the least time between two hellos sent a peer that
// hasn't moved to our new address, at its contacts.
const announceSpacing = time.Minute

// How often this server tries its new address before it announces it
// (answersAt): at once, then after answerRetryFirst, twice as long each
// time, answerRetryMost at most.
var (
	answerRetryFirst = 5 * time.Second
	answerRetryMost  = 5 * time.Minute
)

// NoteAddress notes the address this server federates under, once its
// federation is built (the gateway's rebuilds): when it changed since the
// last, a move begins. Every peer is told, in the background, only once
// this server reaches itself at its new address (MSFP core section 13.1,
// step 2: answersAt); until then its hellos name its former one. A move
// already under way, from before a restart, is checked so again.
func (s *Service) NoteAddress(ctx context.Context) error {
	moved, err := s.Store.NoteFederationAddress(ctx, s.Address, s.now())
	if err != nil {
		return err
	}
	a, err := s.Store.FederationAddress(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.former, s.movedAt = a.Former, a.MovedAt
	s.mu.Unlock()
	if !moved {
		if _, _, ok := s.Moving(); ok && !s.Uncallable {
			s.waitForAnswer()
		}
		return nil
	}
	s.Logger.Info("federation: this server's address changed; paired servers are told once it answers there", "from", a.Former, "to", s.Address)
	// A move to or from an address of a server that can't be called: no
	// peer calls the former, so it isn't kept. To it, peers are told by
	// this server's scheduled hellos, the first at once (Hellos); from it,
	// by a hello to each, as for any move, even those whose requests were
	// signed for this address before: they stored the other since.
	fromUncallable := msfp.Uncallable(a.Former)
	switch {
	case s.Uncallable:
		s.endMove(ctx, "this server can't be called")
	case fromUncallable:
		s.endMove(ctx, "this server can be called again; nothing called its former address")
	}
	if s.Uncallable {
		return nil
	}
	if !fromUncallable && s.allMoved(ctx) {
		// Back to an address every peer still calls (a move undone
		// before any followed it): nothing to wait for.
		s.endMove(ctx, "every peer calls this server's address already")
		return nil
	}
	s.waitForAnswer()
	return nil
}

// waitForAnswer holds back the announcement of this server's address
// until it answers there (answersAt), tried in the background, again and
// again, until it does or this server federates under another address.
// Then every peer is told, at once.
func (s *Service) waitForAnswer() {
	s.mu.Lock()
	s.waiting = true
	s.mu.Unlock()
	go func() {
		wait := answerRetryFirst
		for tries := 1; ; tries++ {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			err := s.answersAt(ctx, s.Address)
			cancel()
			if err == nil {
				break
			}
			if tries == 1 {
				s.Logger.Info("federation: this server doesn't answer at its new address yet; paired servers are told once it does",
					"address", s.Address, "error", err)
			}
			time.Sleep(wait)
			wait = min(2*wait, answerRetryMost)
			// Another address noted since (the operator changed it
			// again: that one's own check takes over. Its storage
			// closed: this server is stopping.
			if a, err := s.Store.FederationAddress(context.Background()); err != nil || a.Current != s.Address {
				return
			}
		}
		s.mu.Lock()
		s.waiting = false
		s.mu.Unlock()
		s.Logger.Info("federation: this server answers at its new address; paired servers are told", "address", s.Address)
		ctx, cancel := context.WithTimeout(Background(context.Background()), time.Minute)
		defer cancel()
		s.each(ctx, func(p *store.Peer) bool { return p.State != store.PeerGone }, func(ctx context.Context, p *store.Peer) {
			s.announce(ctx, p)
		})
	}()
}

// ErrNotOwnAnswer: this server's request to its own address wasn't
// answered under its own key: another server, or none, answers there.
var ErrNotOwnAnswer = errors.New("federation: not this server's own answer")

// answersAt checks that address reaches this server, not another one
// (MSFP core section 13.1, step 2): a hello there, signed with this
// server's key for that address's host, as any call to a peer (refused
// destinations refused, any certificate accepted). This server answers a
// request under its own key with the signed 404 of a key it no longer
// knows (forgotten); the signature covers the request's nonce, so only
// this server, reached there, can have made it.
func (s *Service) answersAt(ctx context.Context, address string) error {
	key := s.Keys.Current()
	self := &store.Peer{Address: address, PublicKey: key.Public()}
	r, err := s.exchange(ctx, s.conns(self, true).client, self, helloPath, helloMessage{State: helloPaired}, &json.RawMessage{}, key)
	switch {
	case err != nil:
		return err
	case r.Status != http.StatusNotFound:
		return fmt.Errorf("%w: it answered %d", ErrNotOwnAnswer, r.Status)
	}
	return nil
}

// Waiting says whether this server's new address is still being checked
// before it's announced (waitForAnswer).
func (s *Service) Waiting() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waiting
}

// named is the address this server's hellos name: its former one while
// its new one doesn't answer yet (MSFP core section 13.1, step 2).
func (s *Service) named() string {
	if former, _, ok := s.Moving(); ok && s.Waiting() {
		return former
	}
	return s.Address
}

// Moving is this server's move of address under way: its former address,
// and when the move began; ok is false when there's none, or it's over.
func (s *Service) Moving() (former string, since time.Time, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.former == "" || s.now().Sub(s.movedAt) >= msfp.AddressOverlap {
		return "", time.Time{}, false
	}
	return s.former, s.movedAt, true
}

// formerHost is the host of this server's former address while a move is
// under way, "" otherwise: what Verifier.Former accepts.
func (s *Service) formerHost() string {
	if former, _, ok := s.Moving(); ok {
		return msfp.Host(former)
	}
	return ""
}

// NotMoved says whether p still signs its requests for our former
// address, and late, whether it has for moveLate.
func (s *Service) NotMoved(p *store.Peer) (notMoved, late bool) {
	former, since, ok := s.Moving()
	if !ok || p.OurAddress != former || p.State == store.PeerGone {
		return false, false
	}
	return true, s.now().Sub(since) >= moveLate
}

// heardAt notes which of our addresses a known peer's request was signed
// for (former: the former one). One on the former is told of the new one
// again; the last to move ends the move.
func (s *Service) heardAt(ctx context.Context, p *store.Peer, former bool) {
	ours := s.Address
	if former {
		old, _, ok := s.Moving()
		if !ok {
			return
		}
		ours = old
		s.announceSoon(p)
	}
	if p.OurAddress == ours {
		return
	}
	if err := s.Store.SetPeerOurAddress(ctx, p.ID, ours); err != nil {
		s.Logger.Error("federation: which address a peer calls couldn't be kept", "error", err)
		return
	}
	p.OurAddress = ours
	if former {
		return
	}
	if _, _, ok := s.Moving(); !ok {
		return
	}
	s.Logger.Info("federation: a peer moved to this server's new address", "name", p.Name)
	if s.allMoved(ctx) {
		s.endMove(ctx, "every peer moved to this server's new address")
	}
}

// allMoved says whether a move is under way that no peer still signs
// for the former address of.
func (s *Service) allMoved(ctx context.Context) bool {
	former, _, ok := s.Moving()
	if !ok {
		return false
	}
	n, err := s.Store.PeersOnAddress(ctx, former)
	return err == nil && n == 0
}

// endMove ends our move: the former address no longer accepted.
func (s *Service) endMove(ctx context.Context, why string) {
	if err := s.Store.EndFederationMove(ctx); err != nil {
		s.Logger.Error("federation: the end of a change of address couldn't be kept", "error", err)
		return
	}
	s.mu.Lock()
	former := s.former
	s.former, s.movedAt = "", time.Time{}
	s.mu.Unlock()
	s.Logger.Info("federation: "+why+"; the former address is no longer accepted", "former", former)
}

// announceSoon tells p, in the background, of our new address in a
// hello, unless one went within announceSpacing.
func (s *Service) announceSoon(p *store.Peer) {
	if !s.announceDue(p.ID) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(Background(context.Background()), 20*time.Second)
		defer cancel()
		s.Check(ctx, p)
	}()
}

// announce tells p of our new address in a hello now (Check), unless one
// went within announceSpacing.
func (s *Service) announce(ctx context.Context, p *store.Peer) {
	if s.announceDue(p.ID) {
		s.Check(ctx, p)
	}
}

// announceDue says whether a hello telling a peer our address may go now,
// and notes it goes: never while that address doesn't answer yet.
func (s *Service) announceDue(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waiting {
		return false // its new address doesn't answer yet: nothing to announce
	}
	now := s.now()
	if last, ok := s.announced[id]; ok && now.Sub(last) < announceSpacing {
		return false
	}
	if s.announced == nil {
		s.announced = map[string]time.Time{}
	}
	s.announced[id] = now
	return true
}

// A peer's change of address, here.

// announced handles the address a peer's signed hello, request or answer,
// names (MSFP core section 13.2): another than the one stored is to be
// confirmed, in the background; the stored one again drops a move not
// confirmed. An address that isn't one is ignored, the stored one kept.
func (s *Service) announcedAddress(ctx context.Context, p *store.Peer, address string) error {
	next, err := msfp.Announced(p.Address, address)
	if err != nil {
		s.Logger.Info("federation: a peer named an address that isn't one; its stored address stays", "name", p.Name)
		return nil
	}
	switch p.Inbound {
	case store.InboundNo:
		// It can't be called: nothing to confirm (MSFP core section 13.2).
		if next == "" {
			return nil
		}
		if err := s.Store.SetPeerAddress(ctx, p.ID, next); err != nil {
			return err
		}
		s.Logger.Info("federation: a peer that can't be called moved", "name", p.Name, "from", p.Address, "to", next)
		p.Address = next
		return nil
	case store.InboundConfirming:
		// It can be called again: confirmed where it says it is now, or
		// where it was (section 7.5).
		next = cmp.Or(next, p.Address)
	}
	if next == "" {
		if address == p.Address && p.Move != nil {
			p.Move = nil
			return s.Store.SetPeerMove(ctx, p.ID, nil)
		}
		return nil
	}
	if p.Move == nil || p.Move.Address != next {
		p.Move = &msfp.Move{Address: next}
		if err := s.Store.SetPeerMove(ctx, p.ID, p.Move); err != nil {
			return err
		}
		s.Logger.Info("federation: a peer says it moved; its new address is checked", "name", p.Name, "from", p.Address, "to", next)
	}
	if !p.Move.Due(s.now()) || !s.startConfirming(p.ID) {
		return nil
	}
	q, m := *p, *p.Move
	go func() {
		defer s.doneConfirming(q.ID)
		ctx, cancel := context.WithTimeout(Background(context.Background()), 30*time.Second)
		defer cancel()
		s.confirmMove(ctx, &q, m)
	}()
	return nil
}

// startConfirming says whether a confirmation of p's new address may
// start: one at a time per peer.
func (s *Service) startConfirming(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.confirming[id] {
		return false
	}
	if s.confirming == nil {
		s.confirming = map[string]bool{}
	}
	s.confirming[id] = true
	return true
}

func (s *Service) doneConfirming(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.confirming, id)
}

// ErrMoveFailed: the hello at a peer's new address got no answer signed
// by its key.
var ErrMoveFailed = fmt.Errorf("%w at its new address", ErrUnreachable)

// confirmMove calls hello at p's new address, signed for its host (MSFP
// core section 13.2, step 2), as any connection to a peer: refused
// destinations refused, any certificate accepted (hello). Answered under
// p's key, the new address is stored, with the pins that answer names,
// those of the old address dropped; otherwise the old one stays, and the
// failure is noted for the next try, an hour later at least.
func (s *Service) confirmMove(ctx context.Context, p *store.Peer, m msfp.Move) error {
	// A revoked key: no call at all, nor an answer under it taken (MSFP
	// core section 12.6). The move waits for its rotation.
	if p.Suspended != nil {
		return ErrSuspended
	}
	now := s.now()
	m.Tried = now
	there := *p
	there.Address, there.Certificates = m.Address, nil
	var raw json.RawMessage
	r, err := s.callOnce(ctx, &there, helloPath, s.ourHello(ctx, p, viewOf(p), false), &raw, s.signer(p))
	var answer msfp.Hello
	switch {
	case err != nil:
	case r.Status != http.StatusOK:
		err = fmt.Errorf("%w: it answered %d", ErrMoveFailed, r.Status)
	default:
		if answer, err = msfp.ReadHello(raw, true); err != nil {
			err = fmt.Errorf("%w: %v", ErrMoveFailed, err)
		}
	}
	if err != nil {
		if m.Failing.IsZero() {
			m.Failing = now
		}
		if e := s.Store.SetPeerMove(ctx, p.ID, &m); e != nil {
			return e
		}
		log := s.Logger.Info
		if m.Warn(now) {
			log = s.Logger.Warn
		}
		log("federation: a peer's new address doesn't answer as it; its old address stays", "name", p.Name,
			"address", m.Address, "since", m.Failing, "error", err)
		return err
	}
	moved, err := s.Store.MovePeer(ctx, p.ID, p.Address, m.Address, msfp.CleanPins(answer.Certificates))
	if err != nil || !moved {
		return err
	}
	callableAgain := p.Inbound == store.InboundConfirming
	if callableAgain {
		// It answers there: called again from now on (MSFP core section
		// 7.5).
		if err := s.Store.SetPeerInbound(ctx, p.ID, store.InboundYes); err != nil {
			return err
		}
		s.Logger.Info("federation: a peer that couldn't be called can be again, its address confirmed", "name", p.Name, "address", m.Address)
	}
	s.Logger.Info("federation: a peer moved, its new address confirmed", "name", p.Name, "from", p.Address, "to", m.Address)
	q, err := s.Store.PeerByID(ctx, p.ID)
	if err != nil {
		return err
	}
	// The rest of what it says, as for any hello; an address it names
	// there is for its next one.
	answer.Address = ""
	if err := s.Store.PeerSeen(ctx, q.ID); err != nil {
		return err
	}
	if err := s.keepHello(ctx, q, answer); err != nil {
		return err
	}
	if callableAgain && q.Usable() {
		// What it holds for us was dropped while it couldn't be called:
		// read again now, not at the next reading of every peer.
		s.SyncSoon(q)
	}
	return nil
}

// maintainAddresses is what time brings to addresses, from Maintain: our
// move over after 30 days; a peer that hasn't moved told again; a peer's
// new address that failed tried again.
func (s *Service) maintainAddresses(ctx context.Context, peers []*store.Peer) {
	s.mu.Lock()
	over := s.former != "" && s.now().Sub(s.movedAt) >= msfp.AddressOverlap
	s.mu.Unlock()
	switch {
	case over:
		s.endMove(ctx, "30 days since this server's address changed")
	case s.allMoved(ctx):
		s.endMove(ctx, "every peer moved to this server's new address")
	}
	now := s.now()
	for _, p := range peers {
		if notMoved, _ := s.NotMoved(p); notMoved && p.Callable() {
			s.announce(ctx, p)
		}
		if p.Move != nil && p.Move.Due(now) && s.startConfirming(p.ID) {
			s.confirmMove(ctx, p, *p.Move)
			s.doneConfirming(p.ID)
		}
	}
}
