package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/msfp"
	"github.com/overfin/server/msfp/wire"
)

// The engine's seals are this server's box, under the engine's contexts
// ("msfp current key"…, msfp.Sealer): Overfin's other secrets are
// sealed under contexts of other words (a row's ID, "tmdb token"…), so
// none opens as another.
type boxSealer struct{ box *seal.Box }

func (b boxSealer) Seal(plain []byte, context string) ([]byte, error) {
	return b.box.Seal(plain, context), nil
}

func (b boxSealer) Open(sealed []byte, context string) ([]byte, error) {
	plain, err := b.box.Open(sealed, context)
	if err != nil {
		return nil, msfp.ErrSealed
	}
	return plain, nil
}

// Sealer seals the engine's keys and secrets with this server's box.
func (m msfpStore) Sealer() msfp.Sealer { return boxSealer{m.s.box} }

// The contexts Overfin's own federation sealed its seeds under, before
// the engine, for the conversion: one couldn't be opened as another.
const (
	federationKeyContext     = "federation key"
	federationNextContext    = "federation next key"
	federationOldContext     = "federation old key"
	federationIDsContext     = "federation ids key"
	federationRevokedContext = "federation revoked key"
)

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

// The contexts the engine seals under (msfp.Sealer), for the conversion.
const (
	msfpCurrent = "msfp current key"
	msfpNext    = "msfp next key"
	msfpOld     = "msfp old key"
	msfpIDs     = "msfp ids secret"
	msfpCode    = "msfp code secret"
)

// goMigrations run, in Go, inside the transaction of the migration of
// their number, after its SQL: what needs this server's box.
var goMigrations = map[int]func(ctx context.Context, tx *sql.Tx, s *Store) error{
	129: convertFederation,
}

// convertFederation turns what Overfin's own federation kept into the
// MSFP engine's records (129_msfp_engine.sql), so that this server keeps
// its identity and its pairings whole: its keys, its address, its
// secrets and every peer, with nothing to pair again.
//
// Its seeds and secrets were sealed with this server's box under
// contexts of Overfin's own ("federation key", a code's ID…); they are
// opened and sealed again with the same box under the engine's
// ("msfp current key"…). A server that never federated has nothing to
// convert: the engine makes its keys at its first start.
func convertFederation(ctx context.Context, tx *sql.Tx, s *Store) error {
	meta := func(name string) ([]byte, error) { return metaValue(ctx, tx, name) }
	reseal := func(name, from, to string) ([]byte, []byte, error) {
		sealed, err := meta(name)
		if err != nil || sealed == nil {
			return nil, nil, err
		}
		plain, err := s.box.Open(sealed, from)
		if err != nil {
			return nil, nil, fmt.Errorf("store: %s doesn't open with this server's key: %w", name, err)
		}
		return s.box.Seal(plain, to), plain, nil
	}
	unix := func(name string) (time.Time, error) {
		v, err := meta(name)
		if err != nil || v == nil {
			return time.Time{}, err
		}
		n, err := strconv.ParseInt(string(v), 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("store: %s: %w", name, err)
		}
		return time.Unix(n, 0).UTC(), nil
	}

	var r msfp.ServerRecord
	var err error
	if r.Current, _, err = reseal("federation-key", federationKeyContext, msfpCurrent); err != nil {
		return err
	}
	if r.Current != nil {
		if err := convertServer(ctx, tx, s, &r, meta, reseal, unix); err != nil {
			return err
		}
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO msfp_server (id, record) VALUES (1, ?)`, string(b)); err != nil {
			return err
		}
	}
	if err := convertPeers(ctx, tx); err != nil {
		return err
	}
	// Codes: their secrets, sealed under each code's ID until now. Times
	// in nanoseconds, as the engine's store keeps them.
	type code struct {
		id     string
		secret []byte
	}
	var codes []code
	rows, err := tx.QueryContext(ctx, `SELECT id, secret FROM pairing_codes`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var c code
		if err := rows.Scan(&c.id, &c.secret); err != nil {
			rows.Close()
			return err
		}
		codes = append(codes, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range codes {
		plain, err := s.box.Open(c.secret, c.id)
		if err != nil {
			// A code that doesn't open can't be redeemed anyway.
			if _, err := tx.ExecContext(ctx, `DELETE FROM pairing_codes WHERE id = ?`, c.id); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE pairing_codes SET secret = ? WHERE id = ?`, s.box.Seal(plain, msfpCode), c.id); err != nil {
			return err
		}
	}
	for _, q := range []string{
		`UPDATE pairing_codes SET created_at = created_at * 1000000000, expires_at = expires_at * 1000000000`,
		`UPDATE forgotten_peers SET forgotten_at = forgotten_at * 1000000000`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	// A key forgotten when this server's key was revoked with no next key
	// was forgotten at the revocation: the engine finds those by its time.
	if !r.RevokedAt.IsZero() {
		if _, err := tx.ExecContext(ctx, `UPDATE forgotten_peers SET forgotten_at = ? WHERE revoked = 1`, r.RevokedAt.UnixNano()); err != nil {
			return err
		}
	}
	return nil
}

// convertServer fills this server's record from meta: its keys, its
// address and its settings.
func convertServer(ctx context.Context, tx *sql.Tx, s *Store, r *msfp.ServerRecord,
	meta func(string) ([]byte, error), reseal func(name, from, to string) ([]byte, []byte, error),
	unix func(string) (time.Time, error)) error {
	var err error
	if r.Next, _, err = reseal("federation-next-key", federationNextContext, msfpNext); err != nil {
		return err
	}
	if r.NextCommitment, err = meta("federation-next-commitment"); err != nil {
		return err
	}
	if r.NextCommitment == nil {
		// A next key Overfin never made (it makes one at each start: none
		// on a server that started once only, before it did): one now.
		next := make([]byte, 32)
		if _, err := rand.Read(next); err != nil {
			return err
		}
		k, err := wire.KeyFromSeed(next)
		if err != nil {
			return err
		}
		r.Next, r.NextCommitment = s.box.Seal(next, msfpNext), wire.Commitment(k.Public())
	}
	if r.Old, _, err = reseal("federation-old-key", federationOldContext, msfpOld); err != nil {
		return err
	}
	if r.Old != nil {
		if r.OldSince, err = unix("federation-rotated-at"); err != nil {
			return err
		}
		flag, err := meta("federation-old-revoked")
		if err != nil {
			return err
		}
		r.OldRevoked = string(flag) == "1"
	}
	// The secret of opaque IDs: a server that never made one (it makes it
	// at its first start with federation) gets one now.
	var ids []byte
	if r.IDsSecret, ids, err = reseal("federation-ids-key", federationIDsContext, msfpIDs); err != nil {
		return err
	}
	if ids == nil {
		ids = make([]byte, 32)
		if _, err := rand.Read(ids); err != nil {
			return err
		}
		r.IDsSecret = s.box.Seal(ids, msfpIDs)
	}
	if r.Revoked, _, err = reseal("federation-revoked-key", federationRevokedContext, msfpOld); err != nil {
		return err
	}
	if r.Revoked != nil {
		if r.RevokedAt, err = unix("federation-revoked-at"); err != nil {
			return err
		}
	}
	// Its address, as last federated under, and a move under way. Overfin
	// kept no mark of having reached itself at a new address: a move is
	// tried again at once (the engine's first Run), its hellos naming the
	// former address until then.
	address, err := meta("federation-address")
	if err != nil {
		return err
	}
	r.Address = string(address)
	if former, err := meta("federation-former-address"); err != nil {
		return err
	} else if former != nil {
		r.Former = string(former)
		if r.MovedAt, err = unix("federation-moved-at"); err != nil {
			return err
		}
	}
	on, err := meta("federation-uncallable")
	if err != nil {
		return err
	}
	made, err := meta("federation-uncallable-address")
	if err != nil {
		return err
	}
	r.Uncallable, r.UncallableAddress = string(on) == "on", string(made)
	if r.Uncallable && r.UncallableAddress != "" {
		r.Address = r.UncallableAddress
	}
	// A move to or from an address of the .invalid form isn't kept as one
	// (no peer calls such an address): the engine never accepts it as a
	// host.
	if wire.Uncallable(r.Address) || r.Former == r.Address {
		r.Former, r.MovedAt = "", time.Time{}
	}
	return nil
}

// convertPeers writes each peer's engine record from its columns, its
// rotations and its wants.
func convertPeers(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, public_key, address, name, state, created_at, paired_at, seen_at,
		max_streams, max_transcodes, max_bitrate_kbps, their_streams, their_transcodes, their_bitrate_kbps, certificates, versions,
		next_commitment, commitment_from, previous_key, previous_until, pending_rotation, suspended_at, refused_key, refused_at,
		our_key, rotation_answer, rotate_sent_at, our_address, new_address, new_address_tried, new_address_failing,
		inbound, calls_failed, titles_seq, offers_seq, commitment_told FROM peers`)
	if err != nil {
		return err
	}
	var peers []msfp.PeerRecord
	at := func(v sql.NullInt64) time.Time {
		if !v.Valid {
			return time.Time{}
		}
		return time.Unix(v.Int64, 0).UTC()
	}
	for rows.Next() {
		var p msfp.PeerRecord
		var state, versions, certificates, pending, inbound, newAddress string
		var created int64
		var paired, seen, ts, tt, tb, previousUntil, suspended, refusedAt, rotateSent, tried, failing sql.NullInt64
		var refusedKey []byte
		if err := rows.Scan(&p.ID, &p.Key, &p.Address, &p.Name, &state, &created, &paired, &seen,
			&p.Limits.Streams, &p.Limits.Transcodes, &p.Limits.BitrateKbps, &ts, &tt, &tb, &certificates, &versions,
			&p.Commitment, &p.CommitmentFrom, &p.PreviousKey, &previousUntil, &pending, &suspended, &refusedKey, &refusedAt,
			&p.OurKey, &p.RotationAnswer, &rotateSent, &p.OurAddress, &newAddress, &tried, &failing,
			&inbound, &p.CallsFailed, &p.CatalogRead.Seq, &p.OffersSeq, &p.CommitmentTold); err != nil {
			rows.Close()
			return err
		}
		p.State, p.Inbound = msfp.PeerState(state), msfp.Inbound(inbound)
		p.Created, p.Paired, p.Seen, p.Suspended = time.Unix(created, 0).UTC(), at(paired), at(seen), at(suspended)
		// A gone peer that was paired comes back with its paired hello.
		p.WasPaired = paired.Valid
		p.PreviousUntil, p.RotateSent = at(previousUntil), at(rotateSent)
		if len(p.PreviousKey) == 0 {
			p.PreviousKey, p.PreviousUntil = nil, time.Time{}
		}
		if len(p.Commitment) == 0 {
			p.Commitment = nil
		}
		if ts.Valid && tt.Valid {
			p.TheirLimits = &msfp.Limits{Streams: int(ts.Int64), Transcodes: int(tt.Int64), BitrateKbps: int(tb.Int64)}
		}
		p.Pins = strings.Fields(certificates)
		if versions != "" {
			var v wire.Versions
			if err := json.Unmarshal([]byte(versions), &v); err != nil {
				rows.Close()
				return fmt.Errorf("store: a peer's versions: %w", err)
			}
			p.Versions = &v
		}
		if pending != "" {
			var r wire.Rotation
			if err := json.Unmarshal([]byte(pending), &r); err != nil {
				rows.Close()
				return fmt.Errorf("store: a peer's pending rotation: %w", err)
			}
			p.Pending = &r
		}
		if len(refusedKey) > 0 {
			p.Refused = &msfp.RefusedKey{Key: refusedKey, At: at(refusedAt)}
		}
		if newAddress != "" {
			// Overfin kept no time of the announcement: its first try, or
			// now.
			p.Move = &msfp.PeerMove{Address: newAddress, Since: at(tried), Tried: at(tried), FailingSince: at(failing)}
			if p.Move.Since.IsZero() {
				p.Move.Since = time.Now().UTC().Truncate(time.Second)
			}
		}
		peers = append(peers, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range peers {
		p := &peers[i]
		// Its rotations, as they took effect here.
		err := eachTx(ctx, tx, func(rows *sql.Rows) error {
			var r wire.Rotation
			var next []byte
			if err := rows.Scan(&r.Old, &r.New, &next, &r.Date, &r.OldSignature, &r.NewSignature); err != nil {
				return err
			}
			if len(next) > 0 {
				r.Next = next
			}
			p.Rotations = append(p.Rotations, r)
			return nil
		}, `SELECT old_key, new_key, next_commitment, date, old_signature, new_signature FROM peer_rotations
			WHERE peer_id = ? ORDER BY at, rowid`, p.ID)
		if err != nil {
			return err
		}
		// Its libraries wanted; read with these, its titles as read are
		// those of these libraries, up to its change titles_seq.
		err = eachTx(ctx, tx, func(rows *sql.Rows) error {
			var w msfp.WantedLibrary
			var offered bool
			if err := rows.Scan(&w.ID, &w.Name, &w.Kind, &offered); err != nil {
				return err
			}
			w.Wanted, w.Gone = true, !offered
			p.Wants = append(p.Wants, w)
			return nil
		}, `SELECT library_id, name, kind, offered FROM peer_wants WHERE peer_id = ? ORDER BY name, library_id`, p.ID)
		if err != nil {
			return err
		}
		var read []string
		for _, w := range p.Wants {
			if !w.Gone {
				read = append(read, w.ID)
			}
		}
		slices.Sort(read)
		if p.CatalogRead.Seq > 0 {
			p.CatalogRead.Wants = read
		}
		b, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE peers SET record = ? WHERE id = ?`, string(b), p.ID); err != nil {
			return err
		}
	}
	return nil
}

// eachTx runs fn on each row of a query in tx.
func eachTx(ctx context.Context, tx *sql.Tx, fn func(*sql.Rows) error, query string, args ...any) error {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
