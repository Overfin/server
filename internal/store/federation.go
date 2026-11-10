package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/overfin/server/internal/ids"
)

const federationKeyContext = "federation key"

// FederationSeed is this server's own federation key, as a seed; ok is
// false until one was made.
func (s *Store) FederationSeed(ctx context.Context) (seed []byte, ok bool, err error) {
	var sealed []byte
	err = s.read.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'federation-key'`).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	seed, err = s.box.Open(sealed, federationKeyContext)
	if err != nil {
		return nil, false, fmt.Errorf("store: the federation key can't be read with this secret key: %w", err)
	}
	return seed, true, nil
}

// KeepFederationSeed stores this server's federation key, unless one is
// there already: it returns the one that's kept, so two starts racing
// agree on one key.
func (s *Store) KeepFederationSeed(ctx context.Context, seed []byte) ([]byte, error) {
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('federation-key', ?)
		ON CONFLICT (name) DO NOTHING`, s.box.Seal(seed, federationKeyContext))
	if err != nil {
		return nil, err
	}
	kept, _, err := s.FederationSeed(ctx)
	return kept, err
}

// PairingCode is a pairing code this server made, without its secret.
type PairingCode struct {
	ID      string    `json:"id"`
	Note    string    `json:"note"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
}

func pairingSecretHash(secret []byte) []byte {
	sum := sha256.Sum256(secret)
	return sum[:]
}

// CreatePairingCode keeps a new pairing code's secret, sealed, until
// expires.
func (s *Store) CreatePairingCode(ctx context.Context, secret []byte, note string, expires time.Time) (*PairingCode, error) {
	c := &PairingCode{ID: ids.New("pcode"), Note: note, Created: s.now().UTC().Truncate(time.Second), Expires: expires.UTC()}
	_, err := s.db.ExecContext(ctx, `INSERT INTO pairing_codes (id, secret_hash, secret, note, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`, c.ID, pairingSecretHash(secret), s.box.Seal(secret, c.ID), note, c.Created.Unix(), expires.Unix())
	if err != nil {
		return nil, err
	}
	return c, nil
}

// PairingCodes lists the live codes, newest first.
func (s *Store) PairingCodes(ctx context.Context) ([]PairingCode, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT id, note, created_at, expires_at FROM pairing_codes
		WHERE expires_at > ? ORDER BY created_at DESC, id`, s.now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PairingCode{}
	for rows.Next() {
		var c PairingCode
		var created, expires int64
		if err := rows.Scan(&c.ID, &c.Note, &created, &expires); err != nil {
			return nil, err
		}
		c.Created, c.Expires = time.Unix(created, 0).UTC(), time.Unix(expires, 0).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeletePairingCode withdraws a code; ErrNotFound if there's none.
func (s *Store) DeletePairingCode(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM pairing_codes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Peer is a server this one is paired, or pairing, with.
type Peer struct {
	ID        string     `json:"id"`
	PublicKey []byte     `json:"-"`
	Address   string     `json:"address"`
	Name      string     `json:"name"`
	State     string     `json:"state"`
	Created   time.Time  `json:"created"`
	Paired    *time.Time `json:"paired,omitempty"`
	Seen      *time.Time `json:"seen,omitempty"`
}

// Peer states.
const (
	PeerOurs   = "ours"
	PeerTheirs = "theirs"
	PeerPaired = "paired"
	PeerGone   = "gone"
)

// ErrPeerExists is returned when a server is already paired, or pairing,
// with this one.
var ErrPeerExists = errors.New("store: that server is already in the list")

// RedeemPairingCode is another server using one of our codes: the code is
// found by its secret's hash, live, and check (given the code's secret)
// must approve. The code is then used up and the server added, waiting
// for our operator ("ours"). ErrNotFound for no such live code, or one
// check refuses; ErrPeerExists if that key is known already (the code
// stays).
func (s *Store) RedeemPairingCode(ctx context.Context, secretHash, publicKey []byte, address, name string,
	check func(secret []byte) bool) (*Peer, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id string
	var sealed []byte
	err = tx.QueryRowContext(ctx, `SELECT id, secret FROM pairing_codes WHERE secret_hash = ? AND expires_at > ?`,
		secretHash, s.now().Unix()).Scan(&id, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	secret, err := s.box.Open(sealed, id)
	if err != nil || !check(secret) {
		return nil, ErrNotFound
	}
	var known int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM peers WHERE public_key = ?`, publicKey).Scan(&known); err != nil {
		return nil, err
	}
	if known > 0 {
		return nil, ErrPeerExists
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM pairing_codes WHERE id = ?`, id); err != nil {
		return nil, err
	}
	p, err := insertPeer(ctx, tx, s.now(), publicKey, address, name, PeerOurs)
	if err != nil {
		return nil, err
	}
	return p, tx.Commit()
}

func insertPeer(ctx context.Context, tx *sql.Tx, now time.Time, publicKey []byte, address, name, state string) (*Peer, error) {
	now = now.UTC().Truncate(time.Second)
	p := &Peer{ID: ids.New("peer"), PublicKey: publicKey, Address: address, Name: name, State: state, Created: now, Seen: &now}
	_, err := tx.ExecContext(ctx, `INSERT INTO peers (id, public_key, address, name, state, created_at, seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, p.ID, publicKey, address, name, state, now.Unix(), now.Unix())
	return p, err
}

// AddPeer adds a server whose code we used, waiting for its operator
// ("theirs"). ErrPeerExists if that key is known already.
func (s *Store) AddPeer(ctx context.Context, publicKey []byte, address, name string) (*Peer, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var known int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM peers WHERE public_key = ?`, publicKey).Scan(&known); err != nil {
		return nil, err
	}
	if known > 0 {
		return nil, ErrPeerExists
	}
	p, err := insertPeer(ctx, tx, s.now(), publicKey, address, name, PeerTheirs)
	if err != nil {
		return nil, err
	}
	return p, tx.Commit()
}

const peerSelect = `SELECT id, public_key, address, name, state, created_at, paired_at, seen_at FROM peers`

func scanPeer(row interface{ Scan(...any) error }) (*Peer, error) {
	var p Peer
	var created int64
	var paired, seen sql.NullInt64
	if err := row.Scan(&p.ID, &p.PublicKey, &p.Address, &p.Name, &p.State, &created, &paired, &seen); err != nil {
		return nil, err
	}
	p.Created = time.Unix(created, 0).UTC()
	if paired.Valid {
		t := time.Unix(paired.Int64, 0).UTC()
		p.Paired = &t
	}
	if seen.Valid {
		t := time.Unix(seen.Int64, 0).UTC()
		p.Seen = &t
	}
	return &p, nil
}

// Peers lists every peer, in the order they were added.
func (s *Store) Peers(ctx context.Context) ([]*Peer, error) {
	rows, err := s.read.QueryContext(ctx, peerSelect+` ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Peer{}
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PeerByID finds a peer by its ID; ErrNotFound if there's none.
func (s *Store) PeerByID(ctx context.Context, id string) (*Peer, error) {
	p, err := scanPeer(s.read.QueryRowContext(ctx, peerSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// PeerByKey finds a peer by its public key; ErrNotFound if there's none.
func (s *Store) PeerByKey(ctx context.Context, publicKey []byte) (*Peer, error) {
	p, err := scanPeer(s.read.QueryRowContext(ctx, peerSelect+` WHERE public_key = ?`, publicKey))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// SetPeerState moves a peer from one state to another, and notes it was
// just heard from: false if it wasn't in from (nothing changes then).
func (s *Store) SetPeerState(ctx context.Context, id, from, to string) (bool, error) {
	now := s.now().Unix()
	res, err := s.db.ExecContext(ctx, `UPDATE peers SET state = ?3, seen_at = ?4,
		paired_at = CASE WHEN ?3 = 'paired' THEN ?4 ELSE paired_at END
		WHERE id = ?1 AND state = ?2`, id, from, to, now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// PeerSeen notes a peer was just heard from, by a signed request or answer.
func (s *Store) PeerSeen(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET seen_at = ? WHERE id = ?`, s.now().Unix(), id)
	return err
}

// DeletePeer removes a peer, ending any pairing at once; ErrNotFound if
// there's none.
func (s *Store) DeletePeer(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM peers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteExpiredPairingCodes removes codes past their time.
func (s *Store) DeleteExpiredPairingCodes(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM pairing_codes WHERE expires_at <= ?`, s.now().Unix())
	return err
}

// PairingSecretHash is how a code is found when redeemed.
func PairingSecretHash(secret []byte) []byte { return pairingSecretHash(secret) }

// PeerOffers lists our libraries offered to a peer, by ID.
func (s *Store) PeerOffers(ctx context.Context, peerID string) ([]string, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT library_id FROM peer_offers WHERE peer_id = ? ORDER BY library_id`, peerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetPeerOffers replaces what we offer a peer.
func (s *Store) SetPeerOffers(ctx context.Context, peerID string, libraryIDs []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM peer_offers WHERE peer_id = ?`, peerID); err != nil {
		return err
	}
	for _, id := range libraryIDs {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO peer_offers (peer_id, library_id) VALUES (?, ?)`, peerID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PeerLibrary is a library a peer offers us.
type PeerLibrary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// PeerWants lists the peer's libraries we want, by name.
func (s *Store) PeerWants(ctx context.Context, peerID string) ([]PeerLibrary, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT library_id, name, kind FROM peer_wants WHERE peer_id = ? ORDER BY name, library_id`, peerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PeerLibrary{}
	for rows.Next() {
		var l PeerLibrary
		if err := rows.Scan(&l.ID, &l.Name, &l.Kind); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// SetPeerWants replaces what we want of a peer's libraries.
func (s *Store) SetPeerWants(ctx context.Context, peerID string, libs []PeerLibrary) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM peer_wants WHERE peer_id = ?`, peerID); err != nil {
		return err
	}
	for _, l := range libs {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO peer_wants (peer_id, library_id, name, kind) VALUES (?, ?, ?, ?)`,
			peerID, l.ID, l.Name, l.Kind); err != nil {
			return err
		}
	}
	return tx.Commit()
}
