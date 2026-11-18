package federation

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/overfin/server/internal/msfp"
	"github.com/overfin/server/internal/store"
)

// Keys are this server's federation keys (MSFP core sections 4 and 12),
// as the Services a server makes over its life share them (each settings
// change rebuilds one): the current key, the old one while a change of key
// is under way, and the next key's commitment. The store holds them, the
// seeds sealed; Keys is what's in effect now.
type Keys struct {
	mu      sync.RWMutex
	current msfp.Key
	// old is the key before current while peers still move from it (nil:
	// no change under way), since when.
	old   *msfp.Key
	since time.Time
	// next is the commitment to the next key, whose seed is in the store
	// or kept offline by the operator.
	next []byte
	// ids is the secret of opaque IDs (Opaque): it never changes, so the
	// IDs peers know stay through a change of key.
	ids []byte
}

// idsLabel starts what the IDs secret is made from, the first time: the
// key Overfin made opaque IDs with before keys could change, so the IDs
// peers know stay the same.
const idsLabel = "overfin-federation-1 ids\n"

// LoadKeys reads this server's federation keys, making the first time
// what isn't there yet: the key, on the very first start; the IDs secret
// and the next key, on the first start of a version that has them.
func LoadKeys(ctx context.Context, st *store.Store) (*Keys, error) {
	k, err := st.FederationKeys(ctx)
	if err != nil {
		return nil, err
	}
	if k.Current == nil {
		if _, err := st.KeepFederationSeed(ctx, msfp.NewKey().Seed()); err != nil {
			return nil, err
		}
	}
	if k.Current == nil || k.IDs == nil {
		k, err = st.FederationKeys(ctx)
		if err != nil {
			return nil, err
		}
		ids := sha256.Sum256(append([]byte(idsLabel), k.Current...))
		if err := st.KeepFederationIDs(ctx, ids[:]); err != nil {
			return nil, err
		}
	}
	if k.NextCommitment == nil {
		next := msfp.NewKey()
		if err := st.KeepFederationNext(ctx, next.Seed(), msfp.Commitment(next.Public())); err != nil {
			return nil, err
		}
	}
	if k, err = st.FederationKeys(ctx); err != nil {
		return nil, err
	}
	return keysOf(k)
}

// keysOf is what the store holds, in effect.
func keysOf(k store.FederationKeys) (*Keys, error) {
	current, err := msfp.KeyFromSeed(k.Current)
	if err != nil {
		return nil, err
	}
	keys := &Keys{current: current, next: k.NextCommitment, ids: k.IDs}
	if k.Old != nil {
		old, err := msfp.KeyFromSeed(k.Old)
		if err != nil {
			return nil, err
		}
		keys.old, keys.since = &old, k.RotatedAt
	}
	return keys, nil
}

// reload reads the keys again from the store, after a change.
func (k *Keys) reload(ctx context.Context, st *store.Store) error {
	stored, err := st.FederationKeys(ctx)
	if err != nil {
		return err
	}
	fresh, err := keysOf(stored)
	if err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.current, k.old, k.since, k.next, k.ids = fresh.current, fresh.old, fresh.since, fresh.next, fresh.ids
	return nil
}

// Current is the key this server is known by, to peers that moved to it.
func (k *Keys) Current() msfp.Key {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.current
}

// Old is the key before the current one, while a change of key is under
// way, and when the change began; false when none is.
func (k *Keys) Old() (msfp.Key, time.Time, bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if k.old == nil {
		return msfp.Key{}, time.Time{}, false
	}
	return *k.old, k.since, true
}

// NextCommitment is the commitment to the next key (MSFP core section
// 12.5).
func (k *Keys) NextCommitment() []byte {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.next
}

// Opaque is an ID for a peer, made from one of ours that it must never
// see nor compute (MSFP playback, section 2.3: version IDs): a MAC, under
// the IDs secret, of what it is, for whom, and our ID. The same for the
// same three, across restarts and changes of key; 128 bits, base64url.
func (k *Keys) Opaque(what, peerID, id string) string {
	k.mu.RLock()
	secret := k.ids
	k.mu.RUnlock()
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(what + "\n" + peerID + "\n" + id))
	return msfp.Encode64(mac.Sum(nil)[:16])
}

// KeyOverlap is how long a change of key lasts at most, and how long a
// peer stays suspended without a valid rotation (MSFP core sections 12.4
// and 12.6): 30 days, fixed.
const KeyOverlap = 30 * 24 * time.Hour

// PreviousHeard is how long a peer's key is still heard after it rotated
// away from it (MSFP core section 12.4): requests already on the way.
const PreviousHeard = 10 * time.Minute

// The next key, as the operator exports it to keep offline (MSFP core
// section 12.5 leaves the form to each server): "overfin-next-key-1."
// then its 32-byte seed, base64url.
const nextKeyPrefix = "overfin-next-key-1."

// Errors of the next key, for the panel to explain.
var (
	// ErrNoNextKey: its seed isn't on the server (exported and deleted),
	// and wasn't given.
	ErrNoNextKey = errors.New("federation: the next key's seed isn't on this server")
	// ErrNotNextKey: what was given isn't the committed next key.
	ErrNotNextKey = errors.New("federation: that isn't this server's next key")
)

// NextKeyText is a next key's seed as the operator exports it.
func NextKeyText(seed []byte) string { return nextKeyPrefix + msfp.Encode64(seed) }

// parseNextKey reads an exported next key: ErrNotNextKey if it isn't one.
func parseNextKey(text string) (msfp.Key, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(text), nextKeyPrefix)
	if !ok {
		return msfp.Key{}, ErrNotNextKey
	}
	seed, ok := msfp.Decode64(rest, 32)
	if !ok {
		return msfp.Key{}, ErrNotNextKey
	}
	return msfp.KeyFromSeed(seed)
}
