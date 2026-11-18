package msfp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"time"

	"github.com/overfin/server/msfp/wire"
)

// hello, both ways (MSFP core section 9.2), and what moves a pairing
// from state to state: accepting, ending, and asking where it stands.

// Hello states, as sent.
const (
	helloPaired  = "paired"
	helloWaiting = "waiting"
	helloEnded   = "ended"
)

// helloMessage is the hello this server sends, and answers; those it
// receives are read by wire.ReadHello.
type helloMessage struct {
	State        string           `json:"state"`
	Core         []int            `json:"core"`
	Parts        map[string][]int `json:"parts"`
	Certificates []string         `json:"certificates,omitempty"`
	Address      string           `json:"address,omitempty"`
	// NextCommitment repeats the commitment the peer holds of us: sent
	// only to a peer that knows our current key (MSFP core section 12.5).
	NextCommitment string `json:"nextCommitment,omitempty"`
	// Inbound is false while this server can't be called (MSFP core
	// section 7.5); absent otherwise.
	Inbound *bool `json:"inbound,omitempty"`
	// KnownKey, in requests of a server that can't be called: the key it
	// checks the peer's answers against (MSFP core section 12.4).
	KnownKey string `json:"knownKey,omitempty"`
	// Rotation is, in such a request, "pending" or "refused" (a string);
	// in answers to such a server, while it knows us by our old key, our
	// rotation statement (a *wire.RotateBody), with, when that key was
	// revoked, the revocation.
	Rotation   any              `json:"rotation,omitempty"`
	Revocation *wire.RevokeBody `json:"revocation,omitempty"`
	// CallsFailed, in answers: our last calls to the caller couldn't
	// connect.
	CallsFailed bool `json:"callsFailed,omitempty"`
}

// endedHello is the hello that ends a pairing, and its answer: state
// alone (MSFP core section 9.2).
type endedHello struct {
	State string `json:"state"`
}

// ourHello is the hello this server sends or answers (answering) p in
// state (paired or waiting): its versions and the address it names; its
// pins, or, while it can't be called, inbound false and, in its
// requests, the key it knows p by (knownKey) and p's rotation it waits
// on; to a peer that knows our current key, our commitment (nil p:
// none). Core and parts are always sent, parts {} when none is on (MSFP
// core section 10.2: a server that speaks version 1 sends both in every
// hello).
func (e *Engine) ourHello(ctx context.Context, state string, p *PeerRecord, answering bool) helloMessage {
	v := e.versions()
	named := e.named(e.address())
	h := helloMessage{State: state, Core: v.Core, Parts: v.Parts, Address: named}
	if p != nil && p.OurKey == KeyCurrent {
		h.NextCommitment = wire.Encode64(e.commitment())
	}
	if !wire.Uncallable(named) {
		h.Certificates = e.ownPins(ctx, answering)
		return h
	}
	no := false
	h.Inbound = &no
	if !answering && p != nil {
		h.KnownKey = wire.Encode64(p.Key)
		switch {
		case p.Pending != nil && wire.SameKey(p.Pending.Old, p.Key):
			h.Rotation = wire.StatePending
		case p.Refused != nil:
			h.Rotation = wire.RotationRefused
		}
	}
	return h
}

// viewOf is how this server describes a pairing to the peer.
func viewOf(p *PeerRecord) string {
	if p.State == Paired {
		return helloPaired
	}
	return helloWaiting
}

// hello is a peer's hello (MSFP core section 9.2).
func (e *Engine) hello(req *request) response {
	p := req.peer
	if p == nil {
		return plain404
	}
	// A body without state, with a member of the wrong type, or with
	// inbound false and an address not of its form, is refused, and
	// nothing is kept.
	msg, err := wire.ReadHello(req.body, false)
	if err != nil || wire.CheckInbound(msg.Uncallable, cmp.Or(msg.Address, p.Address)) != nil {
		return refuse(probNotFound)
	}
	ctx := req.r.Context()
	if msg.State == helloEnded {
		// Forgotten without its key remembered: only the side that ends
		// remembers (MSFP core section 7.4, step 3).
		if err := e.cfg.Store.Update(ctx, func(tx Tx) error { return tx.DeletePeer(p.ID) }); err != nil {
			return refuse(probInternal)
		}
		e.log.Info("msfp: a peer ended the pairing", "peer", p.ID, "name", p.Name)
		e.notify(PeerEnded{Peer: p.ID, Name: p.Name, ByThem: true})
		return answer(endedHello{State: helloEnded})
	}
	var events []Event
	var now PeerRecord
	confirm := false
	err = e.cfg.Store.Update(ctx, func(tx Tx) error {
		events, confirm = nil, false
		q, err := tx.Peer(p.ID)
		if err != nil || q == nil {
			return cmp.Or[error](err, ErrNotFound)
		}
		if msg.State == helloPaired {
			events = append(events, e.theyPaired(q)...)
		}
		ev, c := e.keepHello(q, msg)
		events, confirm = append(events, ev...), c
		q.Seen = e.now().Truncate(time.Second)
		now = *q
		return tx.PutPeer(*q)
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return refuse(probNotFound)
		}
		return refuse(probInternal)
	}
	e.notify(events...)
	if confirm {
		e.confirmSoon(p.ID)
	}
	// From a peer that can't be called: which of our keys it knows us by
	// (MSFP core section 12.4); then our rotation for it to pull.
	if msg.Uncallable {
		now = *e.knownKey(ctx, req, &now, msg)
	}
	h := e.ourHello(ctx, viewOf(&now), &now, true)
	e.pullable(&now, &h)
	h.CallsFailed = e.callsFailedTo(now.ID)
	return answer(h)
}

// theyPaired is a peer saying it has accepted (MSFP core sections 7.1
// and 9.2): if we were waiting for it, or lost it after a pairing both
// had accepted (and it isn't suspended), it is paired.
func (e *Engine) theyPaired(p *PeerRecord) []Event {
	switch {
	case p.State == Theirs, p.State == Gone && p.WasPaired && p.Suspended.IsZero():
		p.State, p.WasPaired, p.Paired = Paired, true, e.now().Truncate(time.Second)
		e.log.Info("msfp: paired", "peer", p.ID, "name", p.Name)
		return []Event{PeerPaired{Peer: p.ID, Name: p.Name}}
	}
	return nil
}

// keepHello keeps what a peer's signed hello, request or answer, says
// of it: whether it can be called (first: where its address is checked,
// or stored at once, depends on it), its address (keepAddress: confirm
// says a confirmation is due), its pins, its versions. Its
// nextCommitment, its form checked by wire.ReadHello, is ignored: a
// commitment comes from pair or a rotation only (MSFP core section 12.5).
func (e *Engine) keepHello(p *PeerRecord, h wire.Hello) (events []Event, confirm bool) {
	events = e.keepInbound(p, h.Uncallable)
	ev, confirm := e.keepAddress(p, h.Address)
	events = append(events, ev...)
	p.Pins = wire.CleanPins(h.Certificates)
	if p.Versions != nil && reflect.DeepEqual(*p.Versions, h.Versions) {
		return events, confirm
	}
	v := h.Versions
	p.Versions = &v
	if core, _ := e.agreed(p); core == 0 {
		e.log.Info("msfp: a peer speaks no version of MSFP's core this server speaks", "peer", p.ID, "name", p.Name)
	}
	return append(events, VersionsChanged{Peer: p.ID, Name: p.Name}), confirm
}

// keepCallsFailed keeps a hello answer's callsFailed: shown on the panel
// (CallsFailed, when it turns true), nothing else (MSFP core section 7.5).
func (e *Engine) keepCallsFailed(p *PeerRecord, failed bool) []Event {
	was := p.CallsFailed
	p.CallsFailed = failed
	if failed && !was {
		e.log.Info("msfp: a peer says this server's calls to it couldn't connect; maybe this server can't be called", "peer", p.ID)
		return []Event{CallsFailed{Peer: p.ID, Name: p.Name}}
	}
	return nil
}

// Accept is this server's operator accepting a peer that redeemed our
// code, after comparing fingerprints: it is paired, and told so at once
// (when it can't be reached now, it learns at its next hello). Errors:
// ErrNotFound, ErrNotWaiting.
func (e *Engine) Accept(ctx context.Context, id string) (PeerView, error) {
	var p PeerRecord
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		q, err := tx.Peer(id)
		switch {
		case err != nil:
			return err
		case q == nil:
			return ErrNotFound
		case q.State != Ours:
			return ErrNotWaiting
		}
		q.State, q.WasPaired, q.Paired = Paired, true, e.now().Truncate(time.Second)
		p = *q
		return tx.PutPeer(*q)
	})
	if err != nil {
		return PeerView{}, err
	}
	e.log.Info("msfp: paired", "peer", p.ID, "name", p.Name)
	e.notify(PeerPaired{Peer: p.ID, Name: p.Name})
	if p.Inbound != InboundYes {
		// It learns at its next scheduled hello (MSFP core section 7.5).
		return e.view(&p), nil
	}
	v, err := e.Check(ctx, id)
	if err != nil {
		e.log.Info("msfp: the peer couldn't be told it is paired; it learns at its next hello", "peer", id, "error", err)
		return e.Peer(ctx, id)
	}
	return v, nil
}

// End is this server's operator ending a pairing, or refusing a waiting
// one, in any state: the peer is deleted, every key of its history
// forgotten, then it is told (MSFP core section 7.4). Error: ErrNotFound.
func (e *Engine) End(ctx context.Context, id string) error {
	var p PeerRecord
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		q, err := tx.Peer(id)
		switch {
		case err != nil:
			return err
		case q == nil:
			return ErrNotFound
		}
		p = *q
		return forgetPeer(tx, q, e.now().Truncate(time.Second), false)
	})
	if err != nil {
		return err
	}
	e.log.Info("msfp: pairing ended", "peer", p.ID, "name", p.Name)
	e.notify(PeerEnded{Peer: p.ID, Name: p.Name})
	if p.Inbound != InboundYes || !p.Suspended.IsZero() {
		// Not told: a peer that can't be called learns by its next hello's
		// signed 404 (MSFP core section 7.5); a suspended one is called
		// no more (section 12.6).
		return nil
	}
	var ans json.RawMessage
	if _, err := e.call(ctx, &p, callHello, endedHello{State: helloEnded}, &ans, false); err != nil {
		e.log.Info("msfp: the other server couldn't be told the pairing ended", "peer", p.ID, "error", err)
	}
	return nil
}

// historyKeys are every key of a peer's history (MSFP core section
// 7.4): its current one, its previous one, those of its rotations, and
// a pending rotation's.
func historyKeys(p *PeerRecord) [][]byte {
	keys := [][]byte{p.Key}
	if p.PreviousKey != nil {
		keys = append(keys, p.PreviousKey)
	}
	if p.Pending != nil {
		keys = append(keys, p.Pending.New)
	}
	for _, r := range p.Rotations {
		keys = append(keys, r.Old, r.New)
	}
	return keys
}

// Check sends a peer a hello now, telling it where the pairing stands
// here, and applies its answer: an answer carrying a valid revocation
// of the peer's key suspends it, and nothing else of it is kept. Errors:
// ErrNotFound, ErrUncallable, ErrSuspended, ErrKeyLost, ErrUnreachable,
// ErrUnknown (its signed 404: it doesn't know us, and is marked gone).
func (e *Engine) Check(ctx context.Context, id string) (PeerView, error) {
	return e.check1(ctx, id, false)
}

// check1 is Check, as the operator asks it, or in the background. While
// this server can't be called, an answer's rotation is applied too (after
// its revocation), and of a suspended peer's answers, that only (MSFP
// core sections 12.4 and 12.6).
func (e *Engine) check1(ctx context.Context, id string, background bool) (PeerView, error) {
	rec, err := e.peerRecord(ctx, id)
	if err != nil {
		return PeerView{}, err
	}
	msg := e.ourHello(ctx, viewOf(rec), rec, false)
	uncallable := msg.Inbound != nil
	var raw json.RawMessage
	r, err := e.call(ctx, rec, callHello, msg, &raw, background)
	if err != nil {
		return PeerView{}, err
	}
	var events []Event
	switch r.status {
	case http.StatusOK:
		// An answer with a member of the wrong type, or inbound false with
		// an address not of its form, changes nothing: as if it weren't
		// signed (MSFP core section 9.2).
		ans, err := wire.ReadHello(raw, true)
		if err == nil {
			err = wire.CheckInbound(ans.Uncallable, cmp.Or(ans.Address, rec.Address))
		}
		if err != nil {
			return PeerView{}, ErrUnreachable.with(err)
		}
		pulled := ans.Pull(rec.Key, e.now())
		if !rec.Suspended.IsZero() || pulled.Revocation != nil {
			// A revocation, in an answer signed with the key it revokes:
			// suspended, and nothing else of that answer kept (MSFP core
			// sections 9.2, 12.6 and 12.7). Any server applies it; one that
			// can't be called then applies its rotation, the only thing it
			// reads of a suspended peer's answers.
			if pulled.Revocation != nil && rec.Suspended.IsZero() {
				ev, err := e.revoked(ctx, id, rec.Key)
				if err != nil {
					return PeerView{}, err
				}
				e.notify(ev...)
			}
			if uncallable {
				e.pulledRotation(ctx, id, pulled.Rotation)
			}
			return e.Peer(ctx, id)
		}
		confirm := false
		err = e.updatePeer(ctx, id, func(q *PeerRecord) {
			var ev []Event
			ev, confirm = e.keepHello(q, ans)
			events = ev
			if ans.State == helloPaired {
				events = append(events, e.theyPaired(q)...)
			}
			events = append(events, e.keepCallsFailed(q, ans.CallsFailed)...)
			q.Seen = e.now().Truncate(time.Second)
		})
		if err != nil {
			return PeerView{}, err
		}
		e.notify(events...)
		if confirm {
			e.confirmSoon(id)
		}
		if uncallable {
			e.pulledRotation(ctx, id, pulled.Rotation)
		}
		return e.Peer(ctx, id)
	case http.StatusNotFound:
		// It doesn't know us, or suspended our key: gone (MSFP core
		// section 9.2). Its pins and versions stay as they were.
		err = e.updatePeer(ctx, id, func(q *PeerRecord) {
			if q.State != Gone {
				q.State = Gone
				events = append(events, PeerGone{Peer: q.ID, Name: q.Name})
				e.log.Info("msfp: a peer no longer knows this server", "peer", q.ID, "name", q.Name)
			}
		})
		if err != nil {
			return PeerView{}, err
		}
		e.notify(events...)
		return PeerView{}, ErrUnknown
	}
	return PeerView{}, refusalError(r.status, r.problem)
}

// pulledRotation applies a rotation pulled from a hello answer, checked
// (wire.Hello.Pull), as a rotate would be (MSFP core section 12.4, step
// 2): once the peer's new key is current, the next hello goes at once,
// with knownKey the new key (step 3).
func (e *Engine) pulledRotation(ctx context.Context, id string, rot *wire.Rotation) {
	if rot == nil {
		return
	}
	state, err := e.receiveRotation(ctx, id, *rot)
	switch {
	case errors.Is(err, errRefused):
	case err != nil:
		e.log.Warn("msfp: a peer's change of key couldn't be kept", "peer", id, "error", err)
	case state == wire.StateRotated:
		e.helloNow(id)
	}
}

// updatePeer changes a peer in a transaction: ErrNotFound when it is
// gone meanwhile.
func (e *Engine) updatePeer(ctx context.Context, id string, change func(*PeerRecord)) error {
	return e.cfg.Store.Update(ctx, func(tx Tx) error {
		q, err := tx.Peer(id)
		if err != nil || q == nil {
			return cmp.Or[error](err, ErrNotFound)
		}
		change(q)
		return tx.PutPeer(*q)
	})
}

// peerRecord reads a peer: ErrNotFound for none.
func (e *Engine) peerRecord(ctx context.Context, id string) (*PeerRecord, error) {
	var p *PeerRecord
	err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		var err error
		p, err = tx.Peer(id)
		return err
	})
	if err == nil && p == nil {
		err = ErrNotFound
	}
	return p, err
}

// checkEvery is how often, at most, a peer in state theirs is sent a
// hello while the panel is open (MSFP core section 7.1, step 7).
const checkEvery = 20 * time.Second

// PanelOpen tells the engine the operator's panel is listing peers now:
// each peer in state theirs gets a hello, at most one every 20 seconds,
// so the panel shows a pairing once the other operator accepted. It
// returns at once; the hellos go in the background.
func (e *Engine) PanelOpen() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*callTimeout)
		defer cancel()
		if e.saysUncallable() {
			return // it asks on its own schedule (scheduledHellos)
		}
		peers, err := e.Peers(ctx)
		if err != nil {
			e.log.Warn("msfp: the peers couldn't be read", "error", err)
			return
		}
		now := e.now()
		e.askedMu.Lock()
		if e.asked == nil {
			e.asked = map[string]time.Time{}
		}
		var due []string
		for _, p := range peers {
			if p.State != Theirs || p.Inbound != InboundYes || now.Sub(e.asked[p.ID]) < checkEvery {
				continue
			}
			e.asked[p.ID] = now
			due = append(due, p.ID)
		}
		e.askedMu.Unlock()
		for _, id := range due {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*callTimeout)
				defer cancel()
				e.check1(ctx, id, true)
			}()
		}
	}()
}

// sortPeers orders peers by name, then ID, for the panel.
func sortPeers(peers []PeerView) {
	slices.SortFunc(peers, func(a, b PeerView) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
}
