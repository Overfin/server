package msfp

import (
	"context"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/overfin/server/msfp/wire"
)

// Engine runs MSFP for one server: pairing and its states, keys, signed
// calls both ways, limits, and the HTTP handlers peers call. The host
// plugs in through Config. One engine per server, for the life of the
// process; it is safe to use from many goroutines at once.
type Engine struct {
	cfg  Config
	log  *slog.Logger
	host string // the host of cfg.Address, as requests are signed for

	// keys are this server's keys, as the store holds them, opened.
	keysMu sync.RWMutex
	keys   serverKeys

	verifier *wire.Verifier
	limits   *callLimits
	conns    *peerClients

	// asked is when each peer in state theirs was last sent a hello
	// because the panel was open (PanelOpen), by ID.
	askedMu sync.Mutex
	asked   map[string]time.Time

	// events waiting to be told, and whether someone is telling them
	// (notify).
	evMu    sync.Mutex
	events  []Event
	telling bool
}

// serverKeys are this server's keys, opened.
type serverKeys struct {
	current    wire.Key
	commitment []byte // the next key's
	ids        []byte // the secret of opaque IDs
}

// Open starts the engine: on the very first start, it makes this
// server's key, its next key and its secret of opaque IDs, and stores
// them sealed. Errors: ErrInvalidAddress, ErrPlainHTTP, ErrSealed (the
// Sealer doesn't open what is stored: not the key it was sealed with).
func Open(ctx context.Context, cfg Config) (*Engine, error) {
	if cfg.Store == nil {
		return nil, errors.New("msfp: Config.Store is required")
	}
	cfg = cfg.withDefaults()
	if cfg.Sealer == nil {
		return nil, errors.New("msfp: Config.Sealer is required, as the store brings none")
	}
	if cfg.PlainHTTPForTests {
		wire.AllowPlainHTTPForTests(true)
		cfg.Logger.Warn("msfp: plain http pairings are on: for tests only, never for a server in use")
	}
	switch err := wire.CheckAddress(cfg.Address); {
	case errors.Is(err, wire.ErrPlainHTTP):
		return nil, ErrPlainHTTP
	case err != nil:
		return nil, ErrInvalidAddress.with(err)
	}
	u, _ := url.Parse(cfg.Address)
	e := &Engine{cfg: cfg, log: cfg.Logger, host: u.Host}
	if e.cfg.Name == nil {
		e.cfg.Name = func() string { return e.host }
	}
	if err := e.loadKeys(ctx); err != nil {
		return nil, err
	}
	// Nonces in memory only (MSFP core section 6.5): requests dated
	// before this start are refused.
	e.verifier = &wire.Verifier{Host: e.host, Seen: wire.NewNonces(e.now()), Admit: e.admit, Forgotten: e.forgotten}
	e.limits = newCallLimits(cfg.Limits)
	e.conns = &peerClients{e: e}
	return e, nil
}

// now is the host's clock, in UTC.
func (e *Engine) now() time.Time { return e.cfg.Clock.Now().UTC() }

// random reads n bytes of the Config's randomness.
func (e *Engine) random(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(e.cfg.Random, b); err != nil {
		return nil, ErrInternal.with(fmt.Errorf("no randomness: %w", err))
	}
	return b, nil
}

// lowerBase32 writes IDs: short, lowercase, safe in URLs and file names.
var lowerBase32 = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// newID is a random ID of 80 bits, prefixed: "p" for peers, "c" for codes.
func (e *Engine) newID(prefix string) (string, error) {
	b, err := e.random(10)
	if err != nil {
		return "", err
	}
	return prefix + lowerBase32.EncodeToString(b), nil
}

// loadKeys reads this server's keys, making them on the very first
// start, and notes the configured address.
func (e *Engine) loadKeys(ctx context.Context) error {
	var rec ServerRecord
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		r, err := tx.Server()
		if err != nil {
			return err
		}
		if r == nil {
			if r, err = e.newServer(); err != nil {
				return err
			}
		}
		// A different address: noted. Telling peers of a move (MSFP core
		// section 13) comes with a later step of the engine.
		if r.Address != e.cfg.Address {
			r.Address = e.cfg.Address
		}
		rec = *r
		return tx.PutServer(*r)
	})
	if err != nil {
		return err
	}
	return e.openKeys(rec)
}

// newServer makes the server record of a first start: the key, the next
// key and the IDs secret, sealed.
func (e *Engine) newServer() (*ServerRecord, error) {
	current, err := e.random(32)
	if err != nil {
		return nil, err
	}
	next, err := e.random(32)
	if err != nil {
		return nil, err
	}
	ids, err := e.random(32)
	if err != nil {
		return nil, err
	}
	nextKey, err := wire.KeyFromSeed(next)
	if err != nil {
		return nil, err
	}
	r := &ServerRecord{NextCommitment: wire.Commitment(nextKey.Public()), Address: e.cfg.Address}
	if r.Current, err = e.cfg.Sealer.Seal(current, sealCurrent); err != nil {
		return nil, err
	}
	if r.Next, err = e.cfg.Sealer.Seal(next, sealNext); err != nil {
		return nil, err
	}
	if r.IDsSecret, err = e.cfg.Sealer.Seal(ids, sealIDs); err != nil {
		return nil, err
	}
	return r, nil
}

// openKeys opens the stored keys and puts them in effect.
func (e *Engine) openKeys(r ServerRecord) error {
	seed, err := e.cfg.Sealer.Open(r.Current, sealCurrent)
	if err != nil {
		return ErrSealed.with(err)
	}
	current, err := wire.KeyFromSeed(seed)
	if err != nil {
		return ErrSealed.with(err)
	}
	ids, err := e.cfg.Sealer.Open(r.IDsSecret, sealIDs)
	if err != nil {
		return ErrSealed.with(err)
	}
	e.keysMu.Lock()
	e.keys = serverKeys{current: current, commitment: slices.Clone(r.NextCommitment), ids: ids}
	e.keysMu.Unlock()
	return nil
}

// current is the key this server is known by.
func (e *Engine) current() wire.Key {
	e.keysMu.RLock()
	defer e.keysMu.RUnlock()
	return e.keys.current
}

// commitment is the commitment to the next key.
func (e *Engine) commitment() []byte {
	e.keysMu.RLock()
	defer e.keysMu.RUnlock()
	return e.keys.commitment
}

// signerFor is the key this server signs with toward p: its current
// one. (The old key toward peers that haven't moved yet comes with key
// rotation, a later step.)
func (e *Engine) signerFor(*PeerRecord) wire.Key { return e.current() }

// The next key (MSFP core section 12.5).

// nextKeyPrefix starts an exported next key; MSFP's one form.
const nextKeyPrefix = "msfp-next-key-1."

// NextKey is the committed next key's fingerprint, and whether its seed
// is still on this server.
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
// operator exported it; its commitment stays. Error: ErrNoNextKey.
func (e *Engine) DeleteNextKey(ctx context.Context) error {
	return e.cfg.Store.Update(ctx, func(tx Tx) error {
		r, err := tx.Server()
		if err != nil {
			return err
		}
		if r == nil || r.Next == nil {
			return ErrNoNextKey
		}
		r.Next = nil
		return tx.PutServer(*r)
	})
}

// serverRecord reads the server record.
func (e *Engine) serverRecord(ctx context.Context) (ServerRecord, error) {
	var rec ServerRecord
	err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		r, err := tx.Server()
		if err != nil {
			return err
		}
		if r == nil {
			return ErrInternal.with(errors.New("no server record"))
		}
		rec = *r
		return nil
	})
	return rec, err
}

// The versions this engine speaks (MSFP core section 10): core 1. The
// parts come with the later steps of the engine (catalog, playback,
// media, video, music); until then it lists none, and answers their
// calls not-supported.
func (e *Engine) versions() wire.Versions {
	return wire.Versions{Core: []int{1}, Parts: map[string][]int{}}
}

// agreed is what this server and a peer use between them: the core
// version (0: none in common) and the parts on.
func (e *Engine) agreed(p *PeerRecord) (core int, parts map[string]int) {
	if p.Versions == nil {
		// Before any hello: the core of the code the pairing started
		// with, no part (MSFP core section 10.3).
		return 1, map[string]int{}
	}
	core, parts, ok := wire.Agreed(e.versions(), *p.Versions)
	if !ok {
		return 0, map[string]int{}
	}
	return core, parts
}

// name is this server's name, as peers show it.
func (e *Engine) name() string { return cleanName(e.cfg.Name(), e.cfg.Address) }
