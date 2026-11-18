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

	// keys are this server's keys, as the store holds them, opened:
	// read under keysMu. recordMu makes every write of the server record
	// one at a time, from its Update to putting what it committed in
	// effect (changeServer), so that keys always hold the record as last
	// committed.
	keysMu   sync.RWMutex
	keys     serverKeys
	recordMu sync.Mutex
	// keyAction makes the operator's key actions (Rotate, KeyStolen) one
	// at a time: a second one while one runs is ErrRotationUnderWay.
	keyAction sync.Mutex

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
	// old is the key before current while peers move from it (nil: no
	// change under way), since oldSince; oldRevoked: it was revoked
	// first (MSFP core sections 12.4 and 12.7, case 1).
	old        *wire.Key
	oldSince   time.Time
	oldRevoked bool
	// revoked is a key revoked with no next key, replaced by current,
	// since revokedAt (nil: none), kept for the answers of section 12.7,
	// case 2.
	revoked   *wire.Key
	revokedAt time.Time
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
	return e.changeServer(ctx, func(tx Tx, r *ServerRecord) error {
		if r.Current == nil {
			made, err := e.newServer()
			if err != nil {
				return err
			}
			*r = *made
		}
		// A different address: noted. Telling peers of a move (MSFP core
		// section 13) comes with a later step of the engine.
		r.Address = e.cfg.Address
		return nil
	})
}

// errUnchanged, from a changeServer's fn, leaves everything as it was;
// changeServer then returns nil.
var errUnchanged = errors.New("msfp: unchanged")

// changeServer runs fn in one Update on the server record (an empty one
// on the very first start), puts the record back, and puts the keys it
// holds in effect. Every write of the server record goes through it:
// recordMu makes them one at a time, from the Update to the keys in
// effect, so a change committed first can't put its keys in effect
// after one committed later. fn decides inside the transaction, from
// the record as it is then: a change that depends on the keys (the end
// of a rotation, a peer moving to our new key) checks there that they
// are still the ones it was made for.
func (e *Engine) changeServer(ctx context.Context, fn func(tx Tx, r *ServerRecord) error) error {
	e.recordMu.Lock()
	defer e.recordMu.Unlock()
	var rec ServerRecord
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		r, err := tx.Server()
		if err != nil {
			return err
		}
		if r == nil {
			r = &ServerRecord{}
		}
		if err := fn(tx, r); err != nil {
			return err
		}
		rec = *r
		return tx.PutServer(*r)
	})
	if errors.Is(err, errUnchanged) {
		return nil
	}
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
	k := serverKeys{commitment: slices.Clone(r.NextCommitment), oldSince: r.OldSince, oldRevoked: r.OldRevoked,
		revokedAt: r.RevokedAt}
	var err error
	if k.current, err = e.openKey(r.Current, sealCurrent); err != nil {
		return err
	}
	if k.ids, err = e.cfg.Sealer.Open(r.IDsSecret, sealIDs); err != nil {
		return ErrSealed.with(err)
	}
	if r.Old != nil {
		old, err := e.openKey(r.Old, sealOld)
		if err != nil {
			return err
		}
		k.old = &old
	}
	if r.Revoked != nil {
		revoked, err := e.openKey(r.Revoked, sealOld)
		if err != nil {
			return err
		}
		k.revoked = &revoked
	}
	e.keysMu.Lock()
	e.keys = k
	e.keysMu.Unlock()
	return nil
}

// openKey opens a sealed seed.
func (e *Engine) openKey(sealed []byte, context string) (wire.Key, error) {
	seed, err := e.cfg.Sealer.Open(sealed, context)
	if err != nil {
		return wire.Key{}, ErrSealed.with(err)
	}
	k, err := wire.KeyFromSeed(seed)
	if err != nil {
		return wire.Key{}, ErrSealed.with(err)
	}
	return k, nil
}

// ourKeys are this server's keys in effect now, as one copy: a statement
// made from them names keys of the same change.
func (e *Engine) ourKeys() serverKeys {
	e.keysMu.RLock()
	defer e.keysMu.RUnlock()
	return e.keys
}

// current is the key this server is known by.
func (e *Engine) current() wire.Key { return e.ourKeys().current }

// commitment is the commitment to the next key.
func (e *Engine) commitment() []byte { return e.ourKeys().commitment }

// signerFor is the key this server signs with toward p: the old one
// while p hasn't moved from it (MSFP core section 12.4), the current one
// otherwise.
func (e *Engine) signerFor(p *PeerRecord) wire.Key {
	k := e.ourKeys()
	if p != nil && p.OurKey == KeyOld && k.old != nil {
		return *k.old
	}
	return k.current
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
