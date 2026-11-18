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
	// Inbound says whether it can be called.
	Inbound Inbound
	// CommitmentFrom is where its next key commitment came from: "pair",
	// "rotation", or "" for none.
	CommitmentFrom string
	// Limits are what it may use here.
	Limits Limits
}

// view is the PeerView of a record.
func (e *Engine) view(p *PeerRecord) PeerView {
	core, parts := e.agreed(p)
	return PeerView{ID: p.ID, Name: p.Name, Address: p.Address, Fingerprint: wire.Fingerprint(p.Key),
		State: p.State, Created: p.Created, Paired: p.Paired, Seen: p.Seen, Suspended: p.Suspended,
		Core: core, Parts: parts, Inbound: p.Inbound, CommitmentFrom: p.CommitmentFrom, Limits: p.Limits}
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
	Address     string
	// NextKey is the committed next key's fingerprint, and NextOnServer
	// whether its seed is still here.
	NextKey      string
	NextOnServer bool
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
	return ServerView{Fingerprint: wire.Fingerprint(e.current().Public()), Address: r.Address,
		NextKey: wire.CommitmentFingerprint(r.NextCommitment), NextOnServer: r.Next != nil,
		Core: v.Core, Parts: v.Parts}, nil
}
