package msfp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/overfin/server/msfp/wire"
)

// Changes of address (MSFP core section 13), this server's and its
// peers'.
//
// This server moves when the host gives it another address (SetAddress,
// or Open with another Config.Address), and when it turns "can't be
// called" on or off (SetUncallable). From A1 to A2: requests signed for
// either host are accepted, for 30 days or until every peer that isn't
// gone signs for A2's; A2 is announced only once this server reaches
// itself there (a hello at A2 signed with its own key, which only this
// server answers, with the signed 404 it gives that key alone), tried at
// once, then from 5 seconds to every 5 minutes; until then its hellos
// name A1. Once reached, every peer is sent a hello at once, and again at
// each contact while it hasn't moved. Its hellos name the pins of both
// addresses while the move lasts. A move to or from an address of the
// .invalid form keeps no former address: nothing calls it there.
//
// A peer whose signed hello names another address than the one stored is
// still called at the stored one until a hello at the new address, signed
// for its host, is answered under the peer's key (after its discovery
// document is fetched there): then the new address, its base and the
// pins of that answer are stored. Failing, the old one stays; the
// confirmation is tried again at most once an hour, and the operator is
// warned after a day. A hello naming the stored address again drops the
// move. A suspended peer's move waits. A peer that can't be called moves
// at once: nothing calls it.

// Times of this server's moves.
const (
	// moveLate is when a peer that hasn't moved to our new address is
	// pointed out (MSFP core section 13.1: "after some days").
	moveLate = 3 * 24 * time.Hour
	// announceSpacing is the least time between two hellos announcing
	// our address to a peer that hasn't moved.
	announceSpacing = time.Minute
	// reachFirst and reachMost: how often this server tries its new
	// address before announcing it: at once, then after reachFirst,
	// twice as long each time, reachMost at most.
	reachFirst = 5 * time.Second
	reachMost  = 5 * time.Minute
)

// addressState is where this server is, as the server record holds it.
type addressState struct {
	address           string // where it is now
	former            string // where it was, while a move lasts ("" for none)
	movedAt           time.Time
	reached           bool // it reached itself at address: the move is announced
	uncallable        bool
	uncallableAddress string
}

// address is where this server is now, in effect.
func (e *Engine) address() addressState {
	e.keysMu.RLock()
	defer e.keysMu.RUnlock()
	return e.addr
}

// named is the address this server names, in its hellos, codes and pair
// requests: its former one while its new one isn't reached yet (MSFP
// core section 13.1, step 2).
func (e *Engine) named(a addressState) string {
	if a.former != "" && !a.reached {
		return a.former
	}
	return a.address
}

// saysUncallable says whether this server says it can't be called: its
// address named is of the .invalid form (MSFP core section 7.5). After
// it turns the setting off, it still says so until its real address is
// reached.
func (e *Engine) saysUncallable() bool { return wire.Uncallable(e.named(e.address())) }

// ourHost is the host requests to this server are signed for now.
func (e *Engine) ourHost() string { return wire.Host(e.address().address) }

// formerHost is the host of this server's former address while it is
// accepted (MSFP core section 13.1, step 1): during a move, for 30 days
// once announced (and until it is), never for one of the .invalid form;
// "" otherwise.
func (e *Engine) formerHost() string {
	a := e.address()
	if a.former == "" || wire.Uncallable(a.former) || a.reached && e.now().Sub(a.movedAt) >= wire.AddressOverlap {
		return ""
	}
	return wire.Host(a.former)
}

// newVerifier makes the request checker for this server's host now, on
// the nonces of the process, when it changed.
func (e *Engine) newVerifier() {
	host := e.ourHost()
	if v := e.verifier.Load(); v != nil && v.Host == host {
		return
	}
	e.verifier.Store(&wire.Verifier{Host: host, Former: e.formerHost, Seen: e.nonces, Admit: e.admit, Forgotten: e.forgotten})
}

// SetAddress is the host's address changing (its settings, a new name or
// port): this server moves there (MSFP core section 13.1). It tries the
// new address at once; once it reaches itself there, every peer is told,
// and requests signed for the former address are still accepted for 30
// days or until every peer moved. While this server can't be called, the
// address is only kept, for when it can again. Errors: ErrInvalidAddress,
// ErrPlainHTTP.
func (e *Engine) SetAddress(ctx context.Context, address string) error {
	switch err := wire.CheckAddress(address); {
	case errors.Is(err, wire.ErrPlainHTTP):
		return ErrPlainHTTP
	case err != nil:
		return ErrInvalidAddress.with(err)
	case wire.Uncallable(address):
		return ErrInvalidAddress.with(errors.New("the address of a server that can't be called is made by the engine (SetUncallable)"))
	}
	e.keysMu.Lock()
	e.configured = address
	e.keysMu.Unlock()
	return e.moveTo(ctx, func(r *ServerRecord) string {
		if r.Uncallable {
			return r.Address
		}
		return address
	})
}

// moveTo moves this server to the address to gives from the record, in
// one Update; nothing when it is there already. A move to be announced
// is tried at once, and announced when this server reaches itself.
func (e *Engine) moveTo(ctx context.Context, to func(*ServerRecord) string) error {
	var from, now string
	err := e.changeServer(ctx, func(tx Tx, r *ServerRecord) error {
		from, now = r.Address, to(r)
		if now == from {
			return errUnchanged
		}
		return e.startMove(tx, r, now)
	})
	if err != nil || from == now {
		return err
	}
	e.log.Info("msfp: this server's address changed; peers are told once it answers there", "from", from, "to", now)
	e.resetHellos()
	if a := e.address(); a.former != "" && !a.reached {
		e.reachNow()
		e.reachAndAnnounce(ctx)
	}
	return nil
}

// startMove moves r to the address to, in tx: the address before it the
// former one, accepted while the move lasts. A move to the .invalid form
// keeps no former (nothing calls it; peers are told by the scheduled
// hellos); a move every peer has made already (none to make) is over at
// once.
func (e *Engine) startMove(tx Tx, r *ServerRecord, to string) error {
	from := r.Address
	r.Address, r.Former, r.MovedAt, r.Reached = to, "", time.Time{}, false
	if from == "" || wire.Uncallable(to) {
		return nil
	}
	peers, err := tx.Peers()
	if err != nil {
		return err
	}
	if notMovedCount(peers, wire.Host(to)) > 0 {
		r.Former, r.MovedAt = from, e.now().Truncate(time.Second)
	}
	return nil
}

// notMoved says whether p counts among the peers that must move to our
// address of host: it isn't gone, and its requests weren't last signed
// for host (MSFP core section 13.1, step 1).
func notMoved(p *PeerRecord, host string) bool { return p.State != Gone && p.OurAddress != host }

func notMovedCount(peers []PeerRecord, host string) int {
	n := 0
	for i := range peers {
		if notMoved(&peers[i], host) {
			n++
		}
	}
	return n
}

// endMove ends r's move: its former address no longer accepted.
func endMove(r *ServerRecord, notMoved int) []Event {
	r.Former, r.MovedAt, r.Reached = "", time.Time{}, false
	return []Event{MoveEnded{NotMoved: notMoved}}
}

// reachNow has this server try its new address at the next chance.
func (e *Engine) reachNow() {
	e.sched.mu.Lock()
	e.sched.reachAt, e.sched.reachWait = e.now(), 0
	e.sched.mu.Unlock()
}

// reachAndAnnounce tries this server's new address, if that is due, and
// once it reaches itself there, announces it: the move is noted as
// reached, and every peer that can be called is sent a hello naming it,
// at once (MSFP core section 13.1, step 2). Failing, it tries again after
// 5 seconds, twice as long each time, every 5 minutes at most.
func (e *Engine) reachAndAnnounce(ctx context.Context) {
	a := e.address()
	if a.former == "" || a.reached {
		return
	}
	now := e.now()
	s := &e.sched
	s.mu.Lock()
	if s.reaching || s.reachAt.IsZero() || now.Before(s.reachAt) {
		s.mu.Unlock()
		return
	}
	s.reaching = true
	s.mu.Unlock()
	err := e.answersAt(ctx, a.address)
	s.mu.Lock()
	s.reaching = false
	if err != nil {
		first := s.reachWait == 0
		s.reachWait = min(max(2*s.reachWait, reachFirst), reachMost)
		s.reachAt = now.Add(s.reachWait)
		s.mu.Unlock()
		if first {
			e.log.Info("msfp: this server doesn't answer at its new address yet; peers are told once it does", "address", a.address, "error", err)
		}
		return
	}
	// A new move: every peer may be told at once again.
	s.reachAt, s.reachWait, s.announced = time.Time{}, 0, nil
	s.mu.Unlock()
	reached := false
	err = e.changeServer(ctx, func(_ Tx, r *ServerRecord) error {
		if r.Address != a.address || r.Former == "" || r.Reached {
			return errUnchanged
		}
		r.Reached, reached = true, true
		return nil
	})
	if err != nil {
		e.log.Warn("msfp: this server's new address couldn't be noted as reached", "error", err)
		return
	}
	if !reached {
		return
	}
	e.log.Info("msfp: this server answers at its new address; peers are told", "address", a.address)
	e.notify(AddressAnnounced{Address: a.address})
	e.resetHellos()
	e.announceAll(ctx)
}

// errNotOwnAnswer: this server's request to its own address wasn't
// answered under its own key: another server, or none, answers there.
var errNotOwnAnswer = errors.New("msfp: not this server's own answer")

// answersAt checks that address reaches this server, not another one: a
// hello there, signed with this server's key for that address's host, as
// any call to a peer (refused destinations refused, any certificate
// accepted). This server answers its own key, alone, with the signed 404
// of a key it doesn't know (forgotten); the signature covers the
// request's nonce, so only this server, reached there, can have made it.
func (e *Engine) answersAt(ctx context.Context, address string) error {
	key := e.current()
	self := &PeerRecord{Address: address, Key: key.Public(), Base: e.cfg.Prefix + defaultBase}
	v := e.versions()
	var raw json.RawMessage
	r, err := e.exchange(ctx, self, callHello, helloMessage{State: helloWaiting, Core: v.Core, Parts: v.Parts}, &raw, key)
	switch {
	case err != nil:
		return err
	case r.status != http.StatusNotFound:
		return fmt.Errorf("%w: it answered %d", errNotOwnAnswer, r.status)
	}
	return nil
}

// announceAll sends a hello naming our address to every peer that hasn't
// moved and can be called, at once, and waits.
func (e *Engine) announceAll(ctx context.Context) {
	host := e.ourHost()
	e.eachPeer(ctx, func(p *PeerRecord) bool {
		return notMoved(p, host) && p.Inbound == InboundYes && p.Suspended.IsZero()
	}, func(ctx context.Context, p *PeerRecord) { e.announce(ctx, p.ID) })
}

// announce tells peer id of our address in a hello now, unless one went
// within announceSpacing.
func (e *Engine) announce(ctx context.Context, id string) {
	if e.announceDue(id) {
		e.check1(ctx, id, true)
	}
}

// announceSoon is announce in the background, for p: a peer that hasn't
// moved, at a contact (MSFP core section 13.1, step 2).
func (e *Engine) announceSoon(p *PeerRecord) {
	if p.Inbound != InboundYes || !p.Suspended.IsZero() || !e.announceDue(p.ID) {
		return
	}
	e.background(func(ctx context.Context) { e.check1(ctx, p.ID, true) })
}

// announceDue says whether a hello announcing our address may go to peer
// id now, and notes it goes: only during a move announced.
func (e *Engine) announceDue(id string) bool {
	if a := e.address(); a.former == "" || !a.reached {
		return false
	}
	now := e.now()
	s := &e.sched
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, ok := s.announced[id]; ok && now.Sub(last) < announceSpacing {
		return false
	}
	if s.announced == nil {
		s.announced = map[string]time.Time{}
	}
	s.announced[id] = now
	return true
}

// heardAt notes which of our addresses a known peer's request was signed
// for (former: our former one): one still on the former is told of the
// new one again; the last to move to the new one ends the move.
func (e *Engine) heardAt(ctx context.Context, p *PeerRecord, former bool) {
	host := e.ourHost()
	if former {
		host = e.formerHost()
		e.announceSoon(p)
	}
	if host == "" || p.OurAddress == host {
		return
	}
	var events []Event
	err := e.changeServer(ctx, func(tx Tx, r *ServerRecord) error {
		events = nil
		q, err := tx.Peer(p.ID)
		if err != nil || q == nil {
			return cmp.Or(err, errUnchanged)
		}
		q.OurAddress = host
		if err := tx.PutPeer(*q); err != nil {
			return err
		}
		if former || r.Former == "" || host != wire.Host(r.Address) {
			return nil
		}
		peers, err := tx.Peers()
		if err != nil {
			return err
		}
		if notMovedCount(peers, host) == 0 {
			events = endMove(r, 0)
		}
		return nil
	})
	if err != nil {
		e.log.Warn("msfp: which address a peer calls couldn't be kept", "peer", p.ID, "error", err)
		return
	}
	p.OurAddress = host
	if len(events) > 0 {
		e.log.Info("msfp: every peer moved to this server's new address; the former one is no longer accepted")
	}
	e.notify(events...)
}

// maintainAddresses is what time does to addresses, in RunOnce: our move
// over after 30 days (or once every peer moved), the peers still on our
// former address pointed out after 3 days and told again; peers' new
// addresses that failed tried again, at most hourly.
func (e *Engine) maintainAddresses(ctx context.Context) error {
	now := e.now()
	if a := e.address(); a.former != "" {
		var events []Event
		err := e.changeServer(ctx, func(tx Tx, r *ServerRecord) error {
			events = nil
			if r.Former == "" {
				return errUnchanged
			}
			peers, err := tx.Peers()
			if err != nil {
				return err
			}
			n := notMovedCount(peers, wire.Host(r.Address))
			switch {
			case n == 0:
				events = endMove(r, 0)
			case r.Reached && now.Sub(r.MovedAt) >= wire.AddressOverlap:
				e.log.Warn("msfp: 30 days since this server's address changed; peers that didn't follow can't reach it", "peers", n)
				events = endMove(r, n)
			default:
				for _, p := range peers {
					if r.Reached && notMoved(&p, wire.Host(r.Address)) && now.Sub(r.MovedAt) >= moveLate && e.lateOnce(p.ID) {
						events = append(events, NotMovedLate{Peer: p.ID, Name: p.Name})
					}
				}
				return errUnchanged
			}
			return nil
		})
		if err != nil {
			return err
		}
		e.notify(events...)
		e.announceAll(ctx)
	}
	e.eachPeer(ctx, func(p *PeerRecord) bool {
		return p.Move != nil && p.Suspended.IsZero() && moveDue(p.Move, now)
	}, func(ctx context.Context, p *PeerRecord) { e.confirmMove(ctx, p.ID) })
	return nil
}

// lateOnce says whether a peer late to move is to be pointed out: once
// per process.
func (e *Engine) lateOnce(id string) bool {
	e.sched.mu.Lock()
	defer e.sched.mu.Unlock()
	if e.sched.late[id] {
		return false
	}
	if e.sched.late == nil {
		e.sched.late = map[string]bool{}
	}
	e.sched.late[id] = true
	return true
}

// A peer's change of address, here.

// moveDue says whether m's confirming hello may go now: the first at
// once, then at most once an hour after a failure.
func moveDue(m *PeerMove, now time.Time) bool {
	return wire.Move{Address: m.Address, Tried: m.Tried, Failing: m.FailingSince}.Due(now)
}

// keepAddress keeps what a peer's signed hello, request or answer, says
// of where it is, after keepInbound (MSFP core sections 7.5 and 13.2):
// another address than the one stored is to be confirmed (confirm), or,
// for a peer that can't be called, stored at once; the stored one again
// drops a move not confirmed. A peer that says it can be called again is
// confirmed where it says it is now, or where it was. An address that
// isn't one is ignored, the stored one kept.
func (e *Engine) keepAddress(p *PeerRecord, address string) (events []Event, confirm bool) {
	next, err := wire.Announced(p.Address, address)
	if err != nil {
		e.log.Info("msfp: a peer named an address that isn't one; its stored address stays", "peer", p.ID)
		return nil, false
	}
	now := e.now().Truncate(time.Second)
	switch p.Inbound {
	case InboundNo:
		// Nothing calls it: nothing to confirm, no former kept, and no
		// pins of it to drop (MSFP core section 13.2).
		if next == "" {
			return nil, false
		}
		from := p.Address
		p.Address, p.Base, p.Move = next, "", nil
		e.log.Info("msfp: a peer that can't be called moved", "peer", p.ID, "from", from, "to", next)
		return []Event{PeerMoved{Peer: p.ID, Name: p.Name, From: from, To: next}}, false
	case InboundConfirming:
		next = cmp.Or(next, p.Address)
	}
	if next == "" {
		if address == p.Address && p.Move != nil {
			p.Move = nil
		}
		return nil, false
	}
	if p.Move == nil || p.Move.Address != next {
		p.Move = &PeerMove{Address: next, Since: now}
		if next != p.Address {
			e.log.Info("msfp: a peer says it moved; its new address is checked", "peer", p.ID, "from", p.Address, "to", next)
		}
	}
	return nil, p.Suspended.IsZero() && moveDue(p.Move, now)
}

// keepInbound keeps whether p can be called, as its signed hello says
// (MSFP core section 7.5). Once it can't, it is never connected to, and
// a move of it waiting is dropped. Once it says it can again, it is
// "confirming": connected to only after a hello at its address answers
// under its key (keepAddress starts that check, confirmMove ends it).
func (e *Engine) keepInbound(p *PeerRecord, uncallable bool) []Event {
	switch {
	case uncallable && p.Inbound != InboundNo:
		p.Inbound, p.Move = InboundNo, nil
		e.forgetCalls(p.ID)
		e.log.Info("msfp: a peer says it can't be called: it can watch what this server shares with it, and shares nothing", "peer", p.ID)
		return []Event{InboundChanged{Peer: p.ID, Name: p.Name, Inbound: InboundNo}}
	case !uncallable && p.Inbound == InboundNo:
		p.Inbound = InboundConfirming
		e.log.Info("msfp: a peer says it can be called again; its address is checked first", "peer", p.ID)
		return []Event{InboundChanged{Peer: p.ID, Name: p.Name, Inbound: InboundConfirming}}
	}
	return nil
}

// confirmSoon is confirmMove in the background.
func (e *Engine) confirmSoon(id string) {
	e.background(func(ctx context.Context) { e.confirmMove(ctx, id) })
}

// confirmMove confirms peer id's new address, one at a time per peer
// (MSFP core section 13.2, step 2): its discovery document fetched
// there, then a hello there, signed for its host, as any connection to a
// peer (refused destinations refused, any certificate accepted). Answered
// under its key, the new address and base are stored, with the pins that
// answer names, the old address's dropped; a peer that said it can be
// called again is called from then on, its catalog read at once. Failing,
// the old address stays, and the failure is noted for the next try, an
// hour later at least; after a day of failures the operator is warned.
// A suspended peer's move waits: nothing is asked of a revoked key.
func (e *Engine) confirmMove(ctx context.Context, id string) {
	if !e.startConfirming(id) {
		return
	}
	// again: it named another address while this one was checked, which
	// waited for this check to end.
	again := false
	defer func() {
		e.doneConfirming(id)
		if again {
			e.confirmSoon(id)
		}
	}()
	p, err := e.peerRecord(ctx, id)
	if err != nil || p.Move == nil || !p.Suspended.IsZero() || !moveDue(p.Move, e.now()) {
		return
	}
	m := *p.Move
	there := *p
	there.Address, there.Base, there.Pins = m.Address, e.fetchBase(ctx, m.Address), nil
	var raw json.RawMessage
	r, err := e.exchange(ctx, &there, callHello, e.ourHello(ctx, viewOf(p), p, false), &raw, e.signerFor(p))
	var ans wire.Hello
	switch {
	case err != nil:
	case r.status != http.StatusOK:
		err = fmt.Errorf("it answered %d", r.status)
	default:
		if ans, err = wire.ReadHello(raw, true); err == nil {
			err = wire.CheckInbound(ans.Uncallable, cmp.Or(ans.Address, m.Address))
		}
	}
	now := e.now().Truncate(time.Second)
	if err != nil {
		var events []Event
		uerr := e.updatePeer(ctx, id, func(q *PeerRecord) {
			events, again = nil, false
			if q.Move == nil || q.Move.Address != m.Address {
				again = q.Move != nil
				return
			}
			q.Move.Tried = now
			if q.Move.FailingSince.IsZero() {
				q.Move.FailingSince = now
			}
			if (wire.Move{Failing: q.Move.FailingSince}).Warn(now) && !q.Move.Warned {
				q.Move.Warned = true
				events = []Event{PeerMoveFailing{Peer: q.ID, Name: q.Name, Address: m.Address, Since: q.Move.FailingSince}}
			}
		})
		if uerr != nil {
			return
		}
		log := e.log.Info
		if len(events) > 0 {
			log = e.log.Warn
		}
		log("msfp: a peer's new address doesn't answer as it; its old address stays", "peer", id, "address", m.Address, "error", err)
		e.notify(events...)
		return
	}
	// A revocation in that answer: suspended, and nothing else of it
	// kept (MSFP core section 12.6).
	if ans.Pull(p.Key, now).Revocation != nil {
		if ev, err := e.revoked(ctx, id, p.Key); err == nil {
			e.notify(ev...)
		}
		return
	}
	var events []Event
	callable, superseded := false, false
	err = e.updatePeer(ctx, id, func(q *PeerRecord) {
		events, callable, superseded = nil, false, false
		if q.Move == nil || !wire.SameKey(q.Key, p.Key) {
			return // its stored address named again, or another key, meanwhile
		}
		// It named yet another address while this one was checked: this
		// one answered under its key, so it is stored all the same (MSFP
		// core section 13.2, step 4), and the newer one is checked next.
		superseded = q.Move.Address != m.Address
		from := q.Address
		q.Address, q.Base, q.Pins = m.Address, there.Base, wire.CleanPins(ans.Certificates)
		if !superseded {
			q.Move = nil
		}
		if from != q.Address {
			events = append(events, PeerMoved{Peer: q.ID, Name: q.Name, From: from, To: q.Address})
		}
		if q.Inbound == InboundConfirming {
			q.Inbound, callable = InboundYes, true
			events = append(events, InboundChanged{Peer: q.ID, Name: q.Name, Inbound: InboundYes})
		}
		// The rest of what it says, as for any hello; an address it names
		// there waits for its next one.
		rest := ans
		rest.Address = ""
		ev, _ := e.keepHello(q, rest)
		events = append(events, ev...)
		if ans.State == helloPaired {
			events = append(events, e.theyPaired(q)...)
		}
		events = append(events, e.keepCallsFailed(q, ans.CallsFailed)...)
		q.Seen = now
	})
	if err != nil {
		return
	}
	for _, ev := range events {
		switch ev := ev.(type) {
		case PeerMoved:
			e.log.Info("msfp: a peer moved, its new address confirmed", "peer", id, "from", ev.From, "to", ev.To)
		case InboundChanged:
			e.log.Info("msfp: a peer that couldn't be called can be again, its address confirmed", "peer", id)
		}
	}
	e.notify(events...)
	if callable {
		// What it holds for us was dropped while it couldn't be called:
		// read again now, not at the next reading.
		e.readCatalogSoon(id)
	}
	again = superseded
}

// readCatalogSoon reads a peer's catalog at once (MSFP catalog section
// 3.2): it can be called again (MSFP core section 7.5).
func (e *Engine) readCatalogSoon(id string) { e.readSoon(id) }

// startConfirming says whether a confirmation of peer id's new address
// may start: one at a time per peer.
func (e *Engine) startConfirming(id string) bool {
	e.sched.mu.Lock()
	defer e.sched.mu.Unlock()
	if e.sched.confirming[id] {
		return false
	}
	if e.sched.confirming == nil {
		e.sched.confirming = map[string]bool{}
	}
	e.sched.confirming[id] = true
	return true
}

func (e *Engine) doneConfirming(id string) {
	e.sched.mu.Lock()
	defer e.sched.mu.Unlock()
	delete(e.sched.confirming, id)
}

// background runs fn in the background, with a time bound, outside any
// request: what a call or a hello started.
func (e *Engine) background(fn func(context.Context)) {
	e.bg.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*callTimeout)
		defer cancel()
		fn(ctx)
	})
}
