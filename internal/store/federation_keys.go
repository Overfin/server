package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/overfin/server/internal/msfp"
)

// This server's federation keys (MSFP core sections 4 and 12), in meta:
// the current key, the next one made in advance, the old one while the
// key is changing, and the key that makes the IDs peers know. Seeds are
// sealed with the secret key, each under a context of its own, as the
// current key always was; the next key's commitment is public.

// Each seed's sealing context: one can't be opened as another.
const (
	federationNextContext = "federation next key"
	federationOldContext  = "federation old key"
	federationIDsContext  = "federation ids key"
)

// FederationKeys are this server's federation keys, as stored.
type FederationKeys struct {
	// Current is the current key's seed.
	Current []byte
	// Next is the next key's seed, nil once the operator deleted it from
	// the server (kept offline); NextCommitment, its commitment, stays.
	Next           []byte
	NextCommitment []byte
	// Old is the key before the current one, while peers still move from
	// it: nil when no change is under way. RotatedAt is when it began.
	Old       []byte
	RotatedAt time.Time
	// OldRevoked: the old key was revoked, the change made after it
	// (MSFP core section 12.7, case 1). Peers that can't be called pull
	// the revocation from hello answers signed with it (section 12.6).
	OldRevoked bool
	// IDs is the secret that makes the IDs peers know this server's
	// things by (federation's opaque IDs): it never changes.
	IDs []byte
}

// metaValue reads one meta value: nil when there's none.
func metaValue(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, name string) ([]byte, error) {
	var v []byte
	err := q.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = ?`, name).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return v, err
}

// FederationKeys reads this server's federation keys. Current is nil
// before the first start made one (KeepFederationSeed); Next and its
// commitment, before KeepFederationNext; IDs, before KeepFederationIDs.
func (s *Store) FederationKeys(ctx context.Context) (FederationKeys, error) {
	var k FederationKeys
	seed, ok, err := s.FederationSeed(ctx)
	if err != nil || !ok {
		return k, err
	}
	k.Current = seed
	open := func(name, context string) ([]byte, error) {
		sealed, err := metaValue(ctx, s.read, name)
		if err != nil || sealed == nil {
			return nil, err
		}
		v, err := s.box.Open(sealed, context)
		if err != nil {
			return nil, fmt.Errorf("store: %s can't be read with this secret key: %w", name, err)
		}
		return v, nil
	}
	if k.Next, err = open("federation-next-key", federationNextContext); err != nil {
		return k, err
	}
	if k.NextCommitment, err = metaValue(ctx, s.read, "federation-next-commitment"); err != nil {
		return k, err
	}
	if k.Old, err = open("federation-old-key", federationOldContext); err != nil {
		return k, err
	}
	if k.IDs, err = open("federation-ids-key", federationIDsContext); err != nil {
		return k, err
	}
	at, err := metaValue(ctx, s.read, "federation-rotated-at")
	if err != nil {
		return k, err
	}
	if at != nil {
		n, err := strconv.ParseInt(string(at), 10, 64)
		if err != nil {
			return k, fmt.Errorf("store: federation-rotated-at: %w", err)
		}
		k.RotatedAt = time.Unix(n, 0).UTC()
	}
	revoked, err := metaValue(ctx, s.read, "federation-old-revoked")
	if err != nil {
		return k, err
	}
	k.OldRevoked = k.Old != nil && string(revoked) == "1"
	return k, nil
}

// KeepFederationIDs stores the IDs secret, unless one is there already.
func (s *Store) KeepFederationIDs(ctx context.Context, secret []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('federation-ids-key', ?)
		ON CONFLICT (name) DO NOTHING`, s.box.Seal(secret, federationIDsContext))
	return err
}

// KeepFederationNext stores the next key and its commitment, unless a
// commitment is there already (its seed may have been deleted, the key
// kept offline): a next key is made once, and changes only with a
// rotation.
func (s *Store) KeepFederationNext(ctx context.Context, seed, commitment []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('federation-next-commitment', ?)
		ON CONFLICT (name) DO NOTHING`, commitment)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil // there's one
	}
	if err := setMeta(ctx, tx, "federation-next-key", s.box.Seal(seed, federationNextContext)); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteFederationNextSeed deletes the next key's seed from the server,
// once the operator exported it: its commitment stays. A rotation then
// needs the seed given back.
func (s *Store) DeleteFederationNextSeed(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM meta WHERE name = 'federation-next-key'`)
	return err
}

func setMeta(ctx context.Context, tx *sql.Tx, name string, value []byte) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES (?, ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, name, value)
	return err
}

// RotateFederationKey makes newSeed (the next key, as committed) the
// current key, keeps the current one as the old key from at, and stores
// nextSeed, with nextCommitment, as the new next key. Every peer that
// knew us by the current key is then to be moved (OurKeyOld). A change
// that was under way must be finished first (FinishFederationRotation).
// revoked says the current key was just revoked (OldRevoked).
func (s *Store) RotateFederationKey(ctx context.Context, newSeed, nextSeed, nextCommitment []byte, at time.Time, revoked bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if old, err := metaValue(ctx, tx, "federation-old-key"); err != nil || old != nil {
		if err == nil {
			err = errors.New("store: a change of federation key is under way")
		}
		return err
	}
	sealed, err := metaValue(ctx, tx, "federation-key")
	if err != nil {
		return err
	}
	current, err := s.box.Open(sealed, federationKeyContext)
	if err != nil {
		return err
	}
	flag := []byte("0")
	if revoked {
		flag = []byte("1")
	}
	for _, m := range []struct {
		name  string
		value []byte
	}{
		{"federation-old-key", s.box.Seal(current, federationOldContext)},
		{"federation-rotated-at", []byte(strconv.FormatInt(at.Unix(), 10))},
		{"federation-old-revoked", flag},
		{"federation-key", s.box.Seal(newSeed, federationKeyContext)},
		{"federation-next-key", s.box.Seal(nextSeed, federationNextContext)},
		{"federation-next-commitment", nextCommitment},
	} {
		if err := setMeta(ctx, tx, m.name, m.value); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE peers SET our_key = 'old', rotation_answer = '', rotate_sent_at = NULL
		WHERE our_key = ''`); err != nil {
		return err
	}
	return tx.Commit()
}

// FinishFederationRotation ends a change of key: the old key is deleted,
// and the peers that hadn't moved from it are lost (OurKeyLost): they
// must pair again.
func (s *Store) FinishFederationRotation(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE name IN ('federation-old-key', 'federation-rotated-at',
		'federation-old-revoked')`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE peers SET our_key = 'lost' WHERE our_key = 'old'`); err != nil {
		return err
	}
	return tx.Commit()
}

// ReplaceFederationKey gives the server a new key and a new next key,
// with no rotation: what a server does after revoking a key whose next
// key is lost (MSFP core section 12.7, case 2). Every peer knew it by the
// revoked key, so every pairing ends, each peer's keys remembered as
// forgotten: they are made again with new codes.
func (s *Store) ReplaceFederationKey(ctx context.Context, newSeed, nextSeed, nextCommitment []byte) error {
	peers, err := s.Peers(ctx)
	if err != nil {
		return err
	}
	for _, p := range peers {
		if err := s.DeletePeer(ctx, p.ID); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE name IN ('federation-old-key', 'federation-rotated-at',
		'federation-old-revoked')`); err != nil {
		return err
	}
	for _, m := range []struct {
		name  string
		value []byte
	}{
		{"federation-key", s.box.Seal(newSeed, federationKeyContext)},
		{"federation-next-key", s.box.Seal(nextSeed, federationNextContext)},
		{"federation-next-commitment", nextCommitment},
	} {
		if err := setMeta(ctx, tx, m.name, m.value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// What this server knows of a peer's keys.

// SetPeerCommitment sets a peer's next key commitment from its hello,
// only if it never had one (trust on first use, MSFP core section
// 12.5): false when it had.
func (s *Store) SetPeerCommitment(ctx context.Context, id string, commitment []byte) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE peers SET next_commitment = ?, commitment_from = 'hello'
		WHERE id = ? AND commitment_from = ''`, commitment, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// RotatePeer makes r.New a peer's current key (MSFP core section 12.3,
// step 5): r.Old, which must be its current key, is its previous one,
// heard until until; r.Next its commitment; the statement kept in its
// history. It lifts a suspension, and drops a pending rotation and a
// refused one. ErrNotFound if the peer isn't there, or isn't on r.Old.
func (s *Store) RotatePeer(ctx context.Context, id string, r msfp.Rotation, until, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE peers SET public_key = ?, previous_key = ?, previous_until = ?,
		next_commitment = ?, commitment_from = 'rotation', pending_rotation = '', suspended_at = NULL,
		refused_key = NULL, refused_at = NULL
		WHERE id = ? AND public_key = ?`, r.New, r.Old, until.Unix(), r.Next, id, r.Old)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO peer_rotations (peer_id, old_key, new_key, next_commitment, date,
		old_signature, new_signature, at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, r.Old, r.New, r.Next, r.Date, r.OldSignature, r.NewSignature, at.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// SetPeerPending keeps a rotation waiting for our operator (r, or nil to
// drop it).
func (s *Store) SetPeerPending(ctx context.Context, id string, r *msfp.Rotation) error {
	text := ""
	if r != nil {
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		text = string(b)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET pending_rotation = ? WHERE id = ?`, text, id)
	return err
}

// PeerRotation is one of a peer's rotations, as it took effect here.
type PeerRotation struct {
	Old, New, Next []byte
	Date           int64
	At             time.Time
}

// PeerRotations lists a peer's rotations, oldest first.
func (s *Store) PeerRotations(ctx context.Context, id string) ([]PeerRotation, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT old_key, new_key, next_commitment, date, at FROM peer_rotations
		WHERE peer_id = ? ORDER BY at, rowid`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PeerRotation
	for rows.Next() {
		var r PeerRotation
		var at int64
		if err := rows.Scan(&r.Old, &r.New, &r.Next, &r.Date, &at); err != nil {
			return nil, err
		}
		r.At = time.Unix(at, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// SuspendPeer marks a peer's key revoked at at (MSFP core section 12.6),
// unless it was already: false then.
func (s *Store) SuspendPeer(ctx context.Context, id string, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE peers SET suspended_at = ? WHERE id = ? AND suspended_at IS NULL`, at.Unix(), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DropPreviousKey stops hearing a peer's previous key at once: it was
// revoked.
func (s *Store) DropPreviousKey(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET previous_key = NULL, previous_until = NULL WHERE id = ?`, id)
	return err
}

// SetPeerRefused notes a rotation that didn't match a peer's commitment,
// for our operator (key nil clears it).
func (s *Store) SetPeerRefused(ctx context.Context, id string, key []byte, at time.Time) error {
	var when any
	if key != nil {
		when = at.Unix()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET refused_key = ?, refused_at = ? WHERE id = ?`, key, when, id)
	return err
}

// What a peer knows of this server's key.

// SetOurKey notes what a peer knows of our key (OurKeyCurrent, …Old,
// …Lost) and its latest answer to our rotate. Moved to our current key,
// it holds our commitment, from the statement.
func (s *Store) SetOurKey(ctx context.Context, id, state, answer string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET our_key = ?2, rotation_answer = ?3,
		commitment_told = CASE WHEN ?2 = '' THEN 1 ELSE commitment_told END WHERE id = ?1`, id, state, answer)
	return err
}

// SetRotateSent notes when we last sent a peer a rotate.
func (s *Store) SetRotateSent(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET rotate_sent_at = ? WHERE id = ?`, at.Unix(), id)
	return err
}

// PeersOnOldKey counts the peers that haven't moved from our old key.
func (s *Store) PeersOnOldKey(ctx context.Context) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM peers WHERE our_key = 'old'`).Scan(&n)
	return n, err
}

func unixTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0).UTC()
	return &t
}
