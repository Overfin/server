package msfp

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"time"

	"github.com/overfin/server/msfp/wire"
)

// Servers that can't be called (MSFP core section 7.5), this one and its
// peers.
//
// This server can't be called when its operator says so (SetUncallable):
// at home behind a router, say, it can connect out, but nothing can
// connect to it. Its address is then https://msfp-<26 characters>.invalid,
// which never resolves, made once and kept; it makes no codes, but
// redeems them; it says so in every pair and hello; and it asks each
// peer, on a schedule, what it would otherwise be told (scheduledHellos):
// that its operator accepted, the peer's changes of key (pulled from its
// answers), its changes of address. It can watch what its peers share;
// it shares nothing. Turned off, it moves back to its real address, as
// any move: announced once it reaches itself there.
//
// A peer that says it can't be called is never connected to (call
// refuses every call); its hellos are answered with our changes of key
// (pullable), and the key it knows us by is read from them (knownKey). A
// peer that says it can be called again is called only once a hello at
// its address answers under its key (confirmMove).

// schedule is what Run does when, and what this server remembers of its
// calls to each peer, in memory, by peer ID.
type schedule struct {
	mu sync.Mutex
	// reachAt is when this server's new address is tried next (zero: not
	// due), after reachWait; reaching while it is.
	reachAt   time.Time
	reachWait time.Duration
	reaching  bool
	// turns are the scheduled hellos' next times, while this server
	// can't be called: none yet is at once; zero is at once too.
	turns map[string]time.Time
	// announced: when each peer was last sent a hello announcing our
	// address; late: those pointed out as late to move.
	announced map[string]time.Time
	late      map[string]bool
	// confirming: a peer's new address being confirmed.
	confirming map[string]bool
	// noConnection: our last call to the peer couldn't connect
	// (callsFailed); failures: calls in a row answered, but not by it, at
	// its stored base; refetched: when its discovery document was
	// fetched again for that.
	noConnection map[string]bool
	failures     map[string]int
	refetched    map[string]time.Time
}

// SetUncallable is this server's operator saying it can't be called (on),
// or can again. On: the address becomes the one of the .invalid form,
// made once and kept, with no former address (nothing calls either);
// live codes are withdrawn, and none is made (ErrNoCodes); every peer is
// sent a hello at once, then on the schedule of MSFP core section 7.5 (Run).
// Off: this server moves back to its address (Config.Address, or the last
// SetAddress): announced as an address change once it reaches itself
// there, until then it still says it can't be called. Error, off:
// ErrInvalidAddress for a server opened without an address (SetAddress
// first).
func (e *Engine) SetUncallable(ctx context.Context, on bool) error {
	made := ""
	if on {
		b, err := e.random(16)
		if err != nil {
			return err
		}
		made = "https://msfp-" + lowerBase32.EncodeToString(b) + ".invalid"
	}
	e.keysMu.RLock()
	configured := e.configured
	e.keysMu.RUnlock()
	if !on && configured == "" {
		return ErrInvalidAddress.with(errors.New("this server has no address to be called at: SetAddress first"))
	}
	err := e.moveTo(ctx, func(r *ServerRecord) string {
		if r.Uncallable == on {
			return r.Address
		}
		r.Uncallable = on
		if !on {
			return configured
		}
		if r.UncallableAddress == "" {
			r.UncallableAddress = made
		}
		return r.UncallableAddress
	})
	if err != nil || !on {
		return err
	}
	return e.cfg.Store.Update(ctx, withdrawCodes)
}

// The hellos of a server that can't be called.

// scheduledHellos sends, while this server says it can't be called,
// every peer whose turn it is a hello (suspended ones included, so that a
// rotation can still reach them), and waits for them; each one's next
// turn is set by where its pairing stands then (wire.HelloWait: 20
// seconds while its operator hasn't accepted, for a day; 5 minutes once
// paired or waiting for ours; an hour once gone), give or take a fifth.
// They count against the peer's limits as any call does, and a peer that
// asked to wait, or doesn't answer, is asked at its next turn.
func (e *Engine) scheduledHellos(ctx context.Context) {
	if !e.saysUncallable() {
		return
	}
	var peers []PeerRecord
	if err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		var err error
		peers, err = tx.Peers()
		return err
	}); err != nil {
		e.log.Warn("msfp: the peers couldn't be read", "error", err)
		return
	}
	now := e.now()
	var wg sync.WaitGroup
	for _, p := range peers {
		if !e.helloDue(p.ID, now) {
			continue
		}
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, 2*callTimeout)
			defer cancel()
			if _, err := e.check1(ctx, p.ID, true); err != nil && !errors.Is(err, ErrUnknown) {
				e.log.Debug("msfp: a scheduled hello failed", "peer", p.ID, "error", err)
			}
			e.helloDone(ctx, p.ID)
		})
	}
	wg.Wait()
}

// helloDue says whether peer id's scheduled hello goes now, and if so,
// notes it under way: its next turn is set once it's answered.
func (e *Engine) helloDue(id string, now time.Time) bool {
	s := &e.sched
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

// helloDone sets peer id's next turn, as its pairing stands after its
// hello; unless helloNow asked for one at once meanwhile.
func (e *Engine) helloDone(ctx context.Context, id string) {
	p, err := e.peerRecord(ctx, id)
	if err != nil {
		return
	}
	now := e.now()
	wait := wire.Vary(wire.HelloWait(p.State == Theirs, p.State == Gone, now.Sub(p.Created)), e.randomFraction())
	s := &e.sched
	s.mu.Lock()
	defer s.mu.Unlock()
	if next, ok := s.turns[id]; ok && next.IsZero() {
		return
	}
	s.turns[id] = now.Add(wait)
}

// helloNow has peer id's next scheduled hello go at the next run.
func (e *Engine) helloNow(id string) {
	s := &e.sched
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turns == nil {
		s.turns = map[string]time.Time{}
	}
	s.turns[id] = time.Time{}
}

// resetHellos has every scheduled hello go at the next run: this server
// now names another address, or can't be called since now.
func (e *Engine) resetHellos() {
	s := &e.sched
	s.mu.Lock()
	s.turns = nil
	s.mu.Unlock()
}

// randomFraction is a number in [0, 1), from the Config's randomness.
func (e *Engine) randomFraction() float64 {
	b, err := e.random(8)
	if err != nil {
		return 0.5
	}
	return float64(binary.BigEndian.Uint64(b)>>11) / (1 << 53)
}

// What this server remembers of its calls to each peer.

// calledPeer notes how a call to p went: whether it connected, for p's
// hello answers (callsFailed: MSFP core section 7.5); and whether it was
// answered by p at its stored base: after failingCalls answers in a row
// that weren't p's, its discovery document is fetched again, at most
// hourly (MSFP core section 11).
func (e *Engine) calledPeer(p *PeerRecord, err error) {
	if p.ID == "" {
		return
	}
	noConnection := errors.Is(err, errNoConnection)
	notPeers := errors.Is(err, ErrUnreachable) && !noConnection
	now := e.now()
	refetch := false
	s := &e.sched
	s.mu.Lock()
	if s.noConnection == nil {
		s.noConnection, s.failures, s.refetched = map[string]bool{}, map[string]int{}, map[string]time.Time{}
	}
	if noConnection {
		s.noConnection[p.ID] = true
	} else if err == nil {
		delete(s.noConnection, p.ID)
	}
	if notPeers {
		s.failures[p.ID]++
		if last, ok := s.refetched[p.ID]; s.failures[p.ID] >= failingCalls && (!ok || now.Sub(last) >= time.Hour) {
			s.refetched[p.ID], s.failures[p.ID], refetch = now, 0, true
		}
	} else if err == nil {
		delete(s.failures, p.ID)
	}
	s.mu.Unlock()
	if refetch {
		id, address := p.ID, p.Address
		e.background(func(ctx context.Context) { e.keepBase(ctx, id, address) })
	}
}

// failingCalls is how many calls in a row, answered but not by the peer,
// make its discovery document fetched again.
const failingCalls = 3

// callsFailedTo says whether this server's last call to peer id couldn't
// connect.
func (e *Engine) callsFailedTo(id string) bool {
	e.sched.mu.Lock()
	defer e.sched.mu.Unlock()
	return e.sched.noConnection[id]
}

// forgetCalls forgets how calls to peer id went: it can't be called now.
func (e *Engine) forgetCalls(id string) {
	e.sched.mu.Lock()
	defer e.sched.mu.Unlock()
	delete(e.sched.noConnection, id)
	delete(e.sched.failures, id)
}
