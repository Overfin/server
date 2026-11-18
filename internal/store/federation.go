package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/overfin/server/internal/ids"
	"github.com/overfin/server/internal/msfp"
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

func pairingSecretHash(secret []byte) []byte { return msfp.SecretHash(secret) }

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
	// Limits is what it may use of this server at once; Theirs, what it
	// says we may use of it (nil until it said).
	Limits PeerLimits  `json:"limits"`
	Theirs *PeerLimits `json:"theirs,omitempty"`
	// Certificates are the pins of the TLS certificates it said, signed,
	// its address serves (msfp.Pin): its own, not publicly trusted
	// perhaps. Never sent to the panel.
	Certificates []string `json:"-"`
	// Versions is what its latest signed hello listed of MSFP's core and
	// parts versions, as JSON; "" before its first.
	Versions string `json:"-"`

	// What this server knows of its keys (MSFP core section 12).
	//
	// NextCommitment is its next key commitment (nil for none), and
	// CommitmentFrom where it came from: CommitmentPair, CommitmentRotation,
	// or "" for none (after a rotation statement without one).
	NextCommitment []byte `json:"-"`
	CommitmentFrom string `json:"-"`
	// PreviousKey is its key before its latest rotation, still heard
	// until PreviousUntil.
	PreviousKey   []byte    `json:"-"`
	PreviousUntil time.Time `json:"-"`
	// Pending is a rotation to a key it has no commitment to, waiting for
	// our operator to confirm the key's fingerprint; nil for none.
	Pending *msfp.Rotation `json:"-"`
	// Suspended is when it revoked its key (nil: it didn't).
	Suspended *time.Time `json:"-"`
	// RefusedKey is a key it tried to rotate to that didn't match its
	// commitment, at RefusedAt: for our operator to see.
	RefusedKey []byte     `json:"-"`
	RefusedAt  *time.Time `json:"-"`

	// What it knows of ours.
	//
	// OurKey is OurKeyCurrent, OurKeyOld (our key is changing and it
	// hasn't moved yet) or OurKeyLost (it didn't move before the old key
	// was dropped).
	OurKey string `json:"-"`
	// RotationAnswer is its latest answer to our rotate: "pending",
	// "refused", or "".
	RotationAnswer string `json:"-"`
	// RotateSent is when we last sent it a rotate.
	RotateSent *time.Time `json:"-"`

	// Addresses (MSFP core section 13).
	//
	// OurAddress is this server's address its latest request was signed
	// for ("" before its first since this was kept): it has moved to
	// ours once that's the current one.
	OurAddress string `json:"-"`
	// Move is an address its signed hello named, not confirmed yet by a
	// hello there answered under its key: Address stays until then. nil
	// for none.
	Move *msfp.Move `json:"-"`

	// Servers that can't be called (MSFP core section 7.5).
	//
	// Inbound is InboundYes, InboundNo (it can't be called: never
	// connected to) or InboundConfirming (it says it can again, not
	// confirmed yet: still never connected to, but by the hello that
	// confirms its address).
	Inbound string `json:"-"`
	// CallsFailed: its latest hello answer said its last calls to this
	// server couldn't connect.
	CallsFailed bool `json:"-"`
}

// Whether a peer can be called (MSFP core section 7.5).
const (
	InboundYes        = "yes"
	InboundNo         = "no"
	InboundConfirming = "confirming"
)

// Callable says whether this server may connect to the peer: it never
// said it can't be called, or it said it can again and that was
// confirmed. A Peer made by hand, not read from the store (a code's
// server), can be.
func (p *Peer) Callable() bool { return p.Inbound != InboundNo && p.Inbound != InboundConfirming }

// Usable says whether the pairing is on: paired, and its key not
// revoked.
func (p *Peer) Usable() bool { return p.State == PeerPaired && p.Suspended == nil }

// Where a peer's next key commitment came from (MSFP core section 12.5).
const (
	CommitmentPair     = "pair"
	CommitmentRotation = "rotation"
)

// What a peer knows of this server's key.
const (
	OurKeyCurrent = ""
	OurKeyOld     = "old"
	OurKeyLost    = "lost"
)

// PeerLimits is what a pairing may use of a server at once: streams,
// transcodes among them, and a bitrate cap (0: none).
type PeerLimits struct {
	Streams     int `json:"streams"`
	Transcodes  int `json:"transcodes"`
	BitrateKbps int `json:"bitrateKbps"`
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
//
// commitment is the server's next key commitment, from its request (nil
// for none); ours goes in the answer, so it holds it. uncallable: its
// request said it can't be called (MSFP core section 7.5).
func (s *Store) RedeemPairingCode(ctx context.Context, secretHash, publicKey []byte, address, name string, commitment []byte,
	uncallable bool, check func(secret []byte) bool) (*Peer, error) {
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
	p, err := insertPeer(ctx, tx, s.now(), publicKey, address, name, PeerOurs, commitment)
	if err != nil {
		return nil, err
	}
	if uncallable {
		if _, err := tx.ExecContext(ctx, `UPDATE peers SET inbound = 'no' WHERE id = ?`, p.ID); err != nil {
			return nil, err
		}
		p.Inbound = InboundNo
	}
	return p, tx.Commit()
}

// insertPeer adds a peer, made by pair: commitment is its own, which pair
// carries both ways (MSFP core section 9.1), as it holds ours.
func insertPeer(ctx context.Context, tx *sql.Tx, now time.Time, publicKey []byte, address, name, state string, commitment []byte) (*Peer, error) {
	now = now.UTC().Truncate(time.Second)
	p := &Peer{ID: ids.New("peer"), PublicKey: publicKey, Address: address, Name: name, State: state, Created: now, Seen: &now,
		Inbound: InboundYes}
	if commitment != nil {
		p.NextCommitment, p.CommitmentFrom = commitment, CommitmentPair
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO peers (id, public_key, address, name, state, created_at, seen_at,
		next_commitment, commitment_from, commitment_told)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`, p.ID, publicKey, address, name, state, now.Unix(), now.Unix(),
		commitment, p.CommitmentFrom)
	return p, err
}

// AddPeer adds a server whose code we used, waiting for its operator
// ("theirs"), with the next key commitment its pair answer gave (nil for
// none). ErrPeerExists if that key is known already.
func (s *Store) AddPeer(ctx context.Context, publicKey []byte, address, name string, commitment []byte) (*Peer, error) {
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
	p, err := insertPeer(ctx, tx, s.now(), publicKey, address, name, PeerTheirs, commitment)
	if err != nil {
		return nil, err
	}
	return p, tx.Commit()
}

const peerSelect = `SELECT id, public_key, address, name, state, created_at, paired_at, seen_at,
	max_streams, max_transcodes, max_bitrate_kbps, their_streams, their_transcodes, their_bitrate_kbps, certificates, versions,
	next_commitment, commitment_from, previous_key, previous_until, pending_rotation, suspended_at, refused_key, refused_at,
	our_key, rotation_answer, rotate_sent_at, our_address, new_address, new_address_tried, new_address_failing,
	inbound, calls_failed FROM peers`

func scanPeer(row interface{ Scan(...any) error }) (*Peer, error) {
	var p Peer
	var created int64
	var paired, seen, ts, tt, tb sql.NullInt64
	var certificates, pending string
	var previousUntil, suspended, refused, rotateSent sql.NullInt64
	var newAddress string
	var newTried, newFailing sql.NullInt64
	if err := row.Scan(&p.ID, &p.PublicKey, &p.Address, &p.Name, &p.State, &created, &paired, &seen,
		&p.Limits.Streams, &p.Limits.Transcodes, &p.Limits.BitrateKbps, &ts, &tt, &tb, &certificates, &p.Versions,
		&p.NextCommitment, &p.CommitmentFrom, &p.PreviousKey, &previousUntil, &pending, &suspended, &p.RefusedKey, &refused,
		&p.OurKey, &p.RotationAnswer, &rotateSent, &p.OurAddress, &newAddress, &newTried, &newFailing,
		&p.Inbound, &p.CallsFailed); err != nil {
		return nil, err
	}
	if newAddress != "" {
		p.Move = &msfp.Move{Address: newAddress}
		if t := unixTime(newTried); t != nil {
			p.Move.Tried = *t
		}
		if t := unixTime(newFailing); t != nil {
			p.Move.Failing = *t
		}
	}
	if previousUntil.Valid {
		p.PreviousUntil = time.Unix(previousUntil.Int64, 0).UTC()
	}
	if pending != "" {
		var r msfp.Rotation
		if err := json.Unmarshal([]byte(pending), &r); err != nil {
			return nil, fmt.Errorf("store: a peer's pending rotation: %w", err)
		}
		p.Pending = &r
	}
	p.Suspended, p.RefusedAt, p.RotateSent = unixTime(suspended), unixTime(refused), unixTime(rotateSent)
	p.Certificates = strings.Fields(certificates)
	if ts.Valid {
		p.Theirs = &PeerLimits{Streams: int(ts.Int64), Transcodes: int(tt.Int64), BitrateKbps: int(tb.Int64)}
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

// PeerByKey finds a peer by its public key, or by its previous one
// (Peer.PreviousKey: whether that is still heard is the caller's to
// say); ErrNotFound if there's none.
func (s *Store) PeerByKey(ctx context.Context, publicKey []byte) (*Peer, error) {
	p, err := scanPeer(s.read.QueryRowContext(ctx, peerSelect+` WHERE public_key = ?1 OR previous_key = ?1
		ORDER BY public_key = ?1 DESC LIMIT 1`, publicKey))
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
//
// Its key is remembered, by its hash (PeerForgotten): its requests are
// answered "unknown", signed.
func (s *Store) DeletePeer(ctx context.Context, id string) error {
	return s.deletePeer(ctx, id, true)
}

// PeerEnded removes a peer that ended the pairing itself (its hello
// "ended"), without remembering its key: the side that ends remembers,
// the other just forgets it (MSFP core section 7.4, step 3), so a new code
// pairs the two again either way. ErrNotFound if there's none.
func (s *Store) PeerEnded(ctx context.Context, id string) error {
	return s.deletePeer(ctx, id, false)
}

func (s *Store) deletePeer(ctx context.Context, id string, remember bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.deletePeerTx(ctx, tx, id, remember, false); err != nil {
		return err
	}
	return tx.Commit()
}

// deletePeerTx is deletePeer in tx, for a caller that removes more with
// it, all or nothing. revoked marks the keys remembered as those of a
// server whose pairing ended because this server's key was stolen
// (ReplaceFederationKey).
func (s *Store) deletePeerTx(ctx context.Context, tx *sql.Tx, id string, remember, revoked bool) error {
	var key, previous []byte
	var pending string
	switch err := tx.QueryRowContext(ctx, `SELECT public_key, previous_key, pending_rotation FROM peers WHERE id = ?`, id).
		Scan(&key, &previous, &pending); {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return err
	}
	if remember {
		// Every key of its history (MSFP core section 7.4): its current
		// one, its previous one, those of its rotations, and the key of
		// a rotation waiting for our operator. None is kept.
		keys := [][]byte{key}
		if previous != nil {
			keys = append(keys, previous)
		}
		if pending != "" {
			var r msfp.Rotation
			if err := json.Unmarshal([]byte(pending), &r); err != nil {
				return err
			}
			keys = append(keys, r.New)
		}
		rows, err := tx.QueryContext(ctx, `SELECT old_key, new_key FROM peer_rotations WHERE peer_id = ?`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var o, n []byte
			if err := rows.Scan(&o, &n); err != nil {
				rows.Close()
				return err
			}
			keys = append(keys, o, n)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, k := range keys {
			hash := sha256.Sum256(k)
			if _, err := tx.ExecContext(ctx, `INSERT INTO forgotten_peers (key_hash, forgotten_at, revoked) VALUES (?, ?, ?)
				ON CONFLICT (key_hash) DO UPDATE SET forgotten_at = excluded.forgotten_at, revoked = excluded.revoked`,
				hash[:], s.now().Unix(), revoked); err != nil {
				return err
			}
		}
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM peers WHERE id = ?`, id)
	return err
}

// PeerForgotten says whether a key is a server's whose pairing with this
// one ended (DeletePeer) and that isn't paired again.
func (s *Store) PeerForgotten(ctx context.Context, publicKey []byte) (bool, error) {
	hash := sha256.Sum256(publicKey)
	var n int
	err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM forgotten_peers f WHERE f.key_hash = ?
		AND NOT EXISTS (SELECT 1 FROM peers p WHERE p.public_key = ?)`, hash[:], publicKey).Scan(&n)
	return n > 0, err
}

// ForgottenByRevocation says whether a key is a server's whose pairing
// ended when this server's key was revoked with no next key
// (ReplaceFederationKey), and that isn't paired again: its hellos get the
// revocation (MSFP core section 12.7, case 2).
func (s *Store) ForgottenByRevocation(ctx context.Context, publicKey []byte) (bool, error) {
	hash := sha256.Sum256(publicKey)
	var n int
	err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM forgotten_peers f WHERE f.key_hash = ? AND f.revoked = 1
		AND NOT EXISTS (SELECT 1 FROM peers p WHERE p.public_key = ?)`, hash[:], publicKey).Scan(&n)
	return n > 0, err
}

// DeleteExpiredPairingCodes removes codes past their time.
func (s *Store) DeleteExpiredPairingCodes(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM pairing_codes WHERE expires_at <= ?`, s.now().Unix())
	return err
}

// PairingSecretHash is how a code is found when redeemed.
func PairingSecretHash(secret []byte) []byte { return pairingSecretHash(secret) }

// ownFirst orders heldTitles (as t) with the library's own titles first:
// what paired servers hold is fetched from TMDB once ours is, so a large
// peer index never holds our own library's pages back.
const ownFirst = `ORDER BY NOT EXISTS (SELECT 1 FROM catalog_items i WHERE i.kind = t.kind AND i.tmdb = t.tmdb)`

// heldTitles, in SQL, are the films and series this server can play or
// will: its library's, and those its paired servers hold for it (by TMDB
// ID), as (kind, tmdb) rows. TMDB's data is kept for all of them alike.
const heldTitles = `SELECT kind, tmdb FROM catalog_items WHERE kind IN ('movie', 'series') AND tmdb IS NOT NULL
	UNION SELECT p.kind, substr(p.title_key, length(p.kind) + 7) FROM peer_titles p
		WHERE p.kind IN ('movie', 'series') AND p.title_key LIKE p.kind || '/tmdb/%'
		AND EXISTS (SELECT 1 FROM peers e WHERE e.id = p.peer_id AND e.state = 'paired' AND e.suspended_at IS NULL)`

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
	// A peer asking for what changed since before this gets the whole list:
	// the libraries' titles it gains or loses aren't changes of theirs.
	// The change of offers takes a number of its own in the change log (a
	// key no reading lists: item/…), so a reading made before it, even
	// with nothing else changed since, is below it and reads the whole
	// list again.
	if _, err := tx.ExecContext(ctx, `INSERT INTO title_changes (title_key, at) VALUES ('item/offers', unixepoch())`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE peers SET offers_seq = (`+changesHead+`) WHERE id = ?`, peerID); err != nil {
		return err
	}
	return tx.Commit()
}

// PeerLibrary is a library a peer offers us.
type PeerLibrary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Label is our operator's own name for its entry in the site's menu,
	// "" for the one made of the peer's names; never sent to peers.
	Label string `json:"-"`
	// Gone, for one we want: it wasn't in the peer's offer at our last
	// reading of its libraries (RenamePeerWants).
	Gone bool `json:"-"`
}

// PeerWants lists the peer's libraries we want, by name.
func (s *Store) PeerWants(ctx context.Context, peerID string) ([]PeerLibrary, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT library_id, name, kind, coalesce(label, ''), NOT offered FROM peer_wants WHERE peer_id = ? ORDER BY name, library_id`, peerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PeerLibrary{}
	for rows.Next() {
		var l PeerLibrary
		if err := rows.Scan(&l.ID, &l.Name, &l.Kind, &l.Label, &l.Gone); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// SetPeerWants replaces what we want of a peer's libraries. Those still
// wanted keep their entry in the menu (its ID, our operator's name).
func (s *Store) SetPeerWants(ctx context.Context, peerID string, libs []PeerLibrary) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ids := make([]string, len(libs))
	for i, l := range libs {
		ids[i] = l.ID
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM peer_wants WHERE peer_id = ? AND library_id NOT IN (SELECT value FROM json_each(?))`,
		peerID, jsonList(ids)); err != nil {
		return err
	}
	// What we read of it under the former wants no longer counts: the next
	// reading is a whole one.
	if _, err := tx.ExecContext(ctx, `UPDATE peers SET titles_seq = 0, wants_gen = wants_gen + 1 WHERE id = ?`, peerID); err != nil {
		return err
	}
	// The titles of a library no longer wanted go with it, at once: not at
	// the next reading of the peer's titles, which may not come soon.
	if _, err := tx.ExecContext(ctx, `DELETE FROM peer_titles WHERE peer_id = ? AND library_id NOT IN (SELECT value FROM json_each(?))`,
		peerID, jsonList(ids)); err != nil {
		return err
	}
	for _, l := range libs {
		// The menu entry's ID comes from the pairing and the library: a
		// library wanted again (unticked, then ticked) gets the same one
		// back, and categories that named it still do.
		sum := sha256.Sum256([]byte(peerID + "\x00" + l.ID))
		menuID := hex.EncodeToString(sum[:16])
		if _, err := tx.ExecContext(ctx, `INSERT INTO peer_wants (peer_id, library_id, name, kind, menu_id)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (peer_id, library_id) DO UPDATE SET name = excluded.name, kind = excluded.kind, offered = 1`,
			peerID, l.ID, l.Name, l.Kind, menuID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RenamePeerWants keeps what a peer now calls the libraries we want of it
// (those it still offers, libs: all it offers), and what it calls
// itself; those it no longer offers are marked gone.
func (s *Store) RenamePeerWants(ctx context.Context, peerID, name string, libs []PeerLibrary) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if name != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE peers SET name = ? WHERE id = ?`, name, peerID); err != nil {
			return err
		}
	}
	ids := make([]string, len(libs))
	for i, l := range libs {
		ids[i] = l.ID
	}
	if _, err := tx.ExecContext(ctx, `UPDATE peer_wants SET offered = library_id IN (SELECT value FROM json_each(?)) WHERE peer_id = ?`,
		jsonList(ids), peerID); err != nil {
		return err
	}
	for _, l := range libs {
		if _, err := tx.ExecContext(ctx, `UPDATE peer_wants SET name = ?, kind = ? WHERE peer_id = ? AND library_id = ?`,
			l.Name, l.Kind, peerID, l.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LabelPeerWant sets our operator's name for a wanted library's entry in
// the menu ("" for the one made of the peer's names).
func (s *Store) LabelPeerWant(ctx context.Context, peerID, libraryID, label string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE peer_wants SET label = nullif(?, '') WHERE peer_id = ? AND library_id = ?`,
		label, peerID, libraryID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MenuLibrary is a library taken from a paired server, as the site's
// menu shows it beside ours.
type MenuLibrary struct {
	// ID is its ID on this server's site, 32 hex digits as ours.
	ID string
	// Name is our operator's name for it, else the peer's: "Films (B)".
	Name string
	Kind string // movies, tvshows… as Jellyfin's
}

// MenuLibraries lists the libraries taken from paired servers that hold
// something for us, in the order they were paired, then by name.
func (s *Store) MenuLibraries(ctx context.Context) ([]MenuLibrary, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT w.menu_id, coalesce(w.label, w.name || ' (' || p.name || ')'), w.kind
		FROM peer_wants w JOIN peers p ON p.id = w.peer_id AND p.state = 'paired' AND p.suspended_at IS NULL
		WHERE w.menu_id IS NOT NULL
			AND EXISTS (SELECT 1 FROM peer_titles t WHERE t.peer_id = w.peer_id AND t.library_id = w.library_id)
		ORDER BY p.paired_at, p.id, w.name, w.library_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MenuLibrary{}
	for rows.Next() {
		var l MenuLibrary
		if err := rows.Scan(&l.ID, &l.Name, &l.Kind); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// PeerTitle is a title a library holds, as servers tell each other: its
// key, its kind and the library.
type PeerTitle struct {
	Key     string `json:"key"`
	Kind    string `json:"kind"`
	Library string `json:"library"`
	// Audio and Subtitles are its copies' languages, as tags ("fr-CA"):
	// a film's or an episode's.
	Audio     []string `json:"audio,omitempty"`
	Subtitles []string `json:"subtitles,omitempty"`
	// With, for an episode, are the keys of the other episodes its copies
	// hold (a file of two episodes), in byte order, at most 16 (MSFP
	// video, section 4.2).
	With []string `json:"with,omitempty"`
	// IDs are the title's IMDb and TheTVDB IDs, as TMDB gives them (MSFP
	// video, section 4.3): a film's or a series', an episode's IMDb ID.
	// Sent only: a paired server's aren't kept, Overfin knowing titles by
	// TMDB's.
	IDs map[string]string `json:"ids,omitempty"`
}

// TitlesInLibraries lists, in key order after after, at most limit of the
// identified titles the given libraries hold (films, series, seasons,
// episodes, by their TMDB keys: files TMDB doesn't know are left out),
// each once, with one of the libraries holding it, by its public ID
// (PublicLibraryIDs, which must have given each library one). full says
// whether it looked at limit titles: there may be more after the last.
func (s *Store) TitlesInLibraries(ctx context.Context, libraries []string, after string, limit int) (titles []PeerTitle, full bool, err error) {
	return s.titlesIn(ctx, libraries, nil, after, limit)
}

// titlesIn is TitlesInLibraries, of keys only when keys isn't nil.
func (s *Store) titlesIn(ctx context.Context, libraries, keys []string, after string, limit int) ([]PeerTitle, bool, error) {
	out := []PeerTitle{}
	if len(libraries) == 0 {
		return out, false, nil
	}
	args := []any{after}
	marks := ""
	for i, l := range libraries {
		if i > 0 {
			marks += ", "
		}
		marks += "?"
		args = append(args, l)
	}
	libArgs := args[1:]
	only := ""
	if keys != nil {
		only = ` AND c.title_key IN (SELECT value FROM json_each(?))`
		args = append(args, jsonList(keys))
	}
	args = append(args, limit)
	// The library given is one holding it, by the ID paired servers know
	// it by (PublicLibraryIDs: only libraries that have one are read), the
	// first in byte order. Only keys of TMDB's (MSFP video, section 4): a
	// title Overfin knows by another source's ID isn't sent.
	rows, err := s.read.QueryContext(ctx, `SELECT c.title_key, min(c.kind), min(f.public_id) FROM catalog_items c
		JOIN federation_libraries f ON f.library_id = c.library_id
		WHERE c.title_key > ? AND c.library_id IN (`+marks+`) AND c.kind IN ('movie', 'series', 'season', 'episode')
			AND (c.title_key GLOB 'movie/tmdb/*' OR c.title_key GLOB 'series/tmdb/*')`+only+`
		GROUP BY c.title_key ORDER BY c.title_key LIMIT ?`, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	looked := 0
	for rows.Next() {
		var t PeerTitle
		if err := rows.Scan(&t.Key, &t.Kind, &t.Library); err != nil {
			return nil, false, err
		}
		looked++
		// In MSFP's one form only: a key in another would be one more
		// title there, which nobody can play.
		if k, ok := msfp.ParseVideoKey(t.Key); ok && k.Kind == t.Kind {
			out = append(out, t)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	rows.Close()
	full := looked == limit
	// Each one's languages: every copy's in those libraries.
	for i := range out {
		if out[i].Kind != "movie" && out[i].Kind != "episode" {
			continue
		}
		langs, err := s.read.QueryContext(ctx, `SELECT audio_tags, subtitle_tags FROM catalog_items
			WHERE title_key = ? AND library_id IN (`+marks+`)`, append([]any{out[i].Key}, libArgs...)...)
		if err != nil {
			return nil, false, err
		}
		for langs.Next() {
			var audio, subs sql.NullString
			if err := langs.Scan(&audio, &subs); err != nil {
				langs.Close()
				return nil, false, err
			}
			out[i].Audio = mergeTags(out[i].Audio, audio.String)
			out[i].Subtitles = mergeTags(out[i].Subtitles, subs.String)
		}
		langs.Close()
		if err := langs.Err(); err != nil {
			return nil, false, err
		}
	}
	if err := s.addWith(ctx, out, marks, libArgs); err != nil {
		return nil, false, err
	}
	if err := s.addOtherIDs(ctx, out); err != nil {
		return nil, false, err
	}
	return out, full, nil
}

// addWith gives each episode of titles the other episodes its copies in
// the libraries (marks, args) hold: those of the same file (range_of).
func (s *Store) addWith(ctx context.Context, titles []PeerTitle, marks string, args []any) error {
	at := map[string]int{}
	var episodes []string
	for i, t := range titles {
		if t.Kind == "episode" {
			at[t.Key] = i
			episodes = append(episodes, t.Key)
		}
	}
	if len(episodes) == 0 {
		return nil
	}
	// The copies of these episodes that hold several, and every episode
	// key of the same file: its first (the item) and the rest (range_of).
	err := s.each(ctx, func(rows *sql.Rows) error {
		var key, other string
		if err := rows.Scan(&key, &other); err != nil {
			return err
		}
		i := at[key]
		if k, ok := msfp.ParseVideoKey(other); ok && k.Kind == "episode" && other != key && !slices.Contains(titles[i].With, other) {
			titles[i].With = append(titles[i].With, other)
		}
		return nil
	}, `WITH files AS (
			SELECT c.title_key AS key, coalesce(c.range_of, c.id) AS file FROM catalog_items c
			WHERE c.kind = 'episode' AND c.title_key IN (SELECT value FROM json_each(?)) AND c.library_id IN (`+marks+`)
				AND (c.range_of IS NOT NULL OR EXISTS (SELECT 1 FROM catalog_items r WHERE r.range_of = c.id)))
		SELECT f.key, o.title_key FROM files f JOIN catalog_items o ON o.id = f.file OR o.range_of = f.file
		WHERE o.title_key IS NOT NULL`, append([]any{jsonList(episodes)}, args...)...)
	if err != nil {
		return err
	}
	for i := range titles {
		if w := titles[i].With; len(w) > 0 {
			slices.Sort(w)
			titles[i].With = w[:min(len(w), msfp.MaxWith)]
		}
	}
	return nil
}

// addOtherIDs gives films, series and episodes their other IDs, as TMDB
// gives them: a film's or a series' IMDb and TheTVDB IDs, an episode's
// IMDb ID (only those known by TMDB's ID of it).
func (s *Store) addOtherIDs(ctx context.Context, titles []PeerTitle) error {
	// Each looked for by its table's key, [key, kind or series, TMDB ID],
	// so that it's found by index.
	at := map[string]int{}
	var titleKeys, episodeKeys [][]any
	for i, t := range titles {
		k, _ := msfp.ParseVideoKey(t.Key)
		switch {
		case k.Kind == "movie" || k.Kind == "series":
			titleKeys = append(titleKeys, []any{t.Key, k.Kind, strconv.FormatInt(k.TMDB, 10)})
		case k.Kind == "episode" && k.EpisodeID > 0:
			episodeKeys = append(episodeKeys, []any{t.Key, strconv.FormatInt(k.TMDB, 10), k.EpisodeID})
		default:
			continue
		}
		at[t.Key] = i
	}
	list := func(l [][]any) string {
		b, _ := json.Marshal(l)
		return string(b)
	}
	add := func(key, scheme string, value sql.NullString) {
		if !value.Valid || !msfp.OtherID(scheme, value.String) {
			return
		}
		t := &titles[at[key]]
		if t.IDs == nil {
			t.IDs = map[string]string{}
		}
		t.IDs[scheme] = value.String
	}
	if len(titleKeys) > 0 {
		err := s.each(ctx, func(rows *sql.Rows) error {
			var key string
			var imdb, tvdb sql.NullString
			if err := rows.Scan(&key, &imdb, &tvdb); err != nil {
				return err
			}
			add(key, "imdb", imdb)
			add(key, "tvdb", tvdb)
			return nil
		}, `SELECT j.value ->> 0, t.imdb, t.tvdb FROM json_each(?) j
			JOIN catalog_titles t ON t.kind = j.value ->> 1 AND t.tmdb = j.value ->> 2`, list(titleKeys))
		if err != nil {
			return err
		}
	}
	if len(episodeKeys) > 0 {
		err := s.each(ctx, func(rows *sql.Rows) error {
			var key string
			var imdb sql.NullString
			if err := rows.Scan(&key, &imdb); err != nil {
				return err
			}
			add(key, "imdb", imdb)
			return nil
		}, `SELECT j.value ->> 0, i.imdb FROM json_each(?) j
			JOIN episode_imdb i ON i.series = j.value ->> 1 AND i.tmdb_id = j.value ->> 2
			WHERE i.imdb IS NOT NULL`, list(episodeKeys))
		if err != nil {
			return err
		}
	}
	return nil
}

// mergeTags adds a JSON list's tags to tags, each once.
func mergeTags(tags []string, list string) []string {
	var more []string
	json.Unmarshal([]byte(list), &more)
	for _, t := range more {
		if !slices.Contains(tags, t) {
			tags = append(tags, t)
		}
	}
	return tags
}

// SetPeerTitles replaces what we know a peer holds for us, and notes when:
// PutPeerTitles and EndPeerTitles at once.
func (s *Store) SetPeerTitles(ctx context.Context, peerID string, titles []PeerTitle) error {
	reading := PeerReading()
	if err := s.PutPeerTitles(ctx, peerID, reading, titles); err != nil {
		return err
	}
	return s.EndPeerTitles(ctx, peerID, reading)
}

var lastReading atomic.Int64

// PeerReading numbers a reading of a peer's titles: each one's number
// exceeds those before it, those of earlier runs too (it's the time, in
// nanoseconds, or one more than the last).
func PeerReading() int64 {
	for {
		last, now := lastReading.Load(), time.Now().UnixNano()
		if now <= last {
			now = last + 1
		}
		if lastReading.CompareAndSwap(last, now) {
			return now
		}
	}
}

// PutPeerTitles keeps a page of what a peer holds for us, as seen by
// reading (a number later readings exceed). A peer's titles come a page
// at a time, however many it has: none is held in memory whole.
func (s *Store) PutPeerTitles(ctx context.Context, peerID string, reading int64, titles []PeerTitle) error {
	if len(titles) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// When each arrived is kept: what the peer held already isn't new.
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO peer_titles (peer_id, title_key, kind, library_id, audio, subtitles, with_keys, added_at, seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (peer_id, title_key) DO UPDATE SET kind = excluded.kind, library_id = excluded.library_id,
			audio = excluded.audio, subtitles = excluded.subtitles, with_keys = excluded.with_keys, seen = max(seen, excluded.seen)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := s.now().Unix()
	for _, t := range titles {
		if _, err := stmt.ExecContext(ctx, peerID, t.Key, t.Kind, t.Library, jsonList(t.Audio), jsonList(t.Subtitles), jsonList(t.With), now, reading); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// EndPeerTitles closes a whole reading of a peer's titles: those it
// didn't see are no longer held for us. A reading cut short never ends,
// so what we knew stays, with what it added.
func (s *Store) EndPeerTitles(ctx context.Context, peerID string, reading int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM peer_titles WHERE peer_id = ? AND seen < ?`, peerID, reading); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE peers SET titles_at = ? WHERE id = ?`, s.now().Unix(), peerID); err != nil {
		return err
	}
	return tx.Commit()
}

// PeerTitleCounts counts, per peer, the films and series we know it holds
// for us.
func (s *Store) PeerTitleCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT peer_id, count(*) FROM peer_titles WHERE kind IN ('movie', 'series') GROUP BY peer_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// TitlesOfPeer lists what we know a peer holds for us, in key order.
func (s *Store) TitlesOfPeer(ctx context.Context, peerID string) ([]PeerTitle, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT title_key, kind, library_id, audio, subtitles, with_keys FROM peer_titles
		WHERE peer_id = ? ORDER BY title_key`, peerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PeerTitle{}
	for rows.Next() {
		var t PeerTitle
		var audio, subs, with sql.NullString
		if err := rows.Scan(&t.Key, &t.Kind, &t.Library, &audio, &subs, &with); err != nil {
			return nil, err
		}
		t.Audio, t.Subtitles, t.With = mergeTags(nil, audio.String), mergeTags(nil, subs.String), mergeTags(nil, with.String)
		out = append(out, t)
	}
	return out, rows.Err()
}

// PeerWith is what a peer said of one of its episodes' copies: the other
// episodes it holds (MSFP video, section 4.2); none for a title it
// doesn't hold for us.
func (s *Store) PeerWith(ctx context.Context, peerID, key string) ([]string, error) {
	var with sql.NullString
	err := s.read.QueryRowContext(ctx, `SELECT with_keys FROM peer_titles WHERE peer_id = ? AND title_key = ?`, peerID, key).Scan(&with)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return mergeTags(nil, with.String), err
}

// SetPeerLimits sets what a peer may use of this server at once.
func (s *Store) SetPeerLimits(ctx context.Context, peerID string, l PeerLimits) error {
	res, err := s.db.ExecContext(ctx, `UPDATE peers SET max_streams = ?, max_transcodes = ?, max_bitrate_kbps = ? WHERE id = ?`,
		l.Streams, l.Transcodes, l.BitrateKbps, peerID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetTheirLimits keeps what a peer says we may use of it.
func (s *Store) SetTheirLimits(ctx context.Context, peerID string, l PeerLimits) error {
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET their_streams = ?, their_transcodes = ?, their_bitrate_kbps = ? WHERE id = ?`,
		l.Streams, l.Transcodes, l.BitrateKbps, peerID)
	return err
}

// SetPeerCertificates keeps the certificate pins a peer gave in its last
// signed hello, replacing those it gave before (none: nil).
func (s *Store) SetPeerCertificates(ctx context.Context, peerID string, pins []string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET certificates = ? WHERE id = ?`, strings.Join(pins, " "), peerID)
	return err
}

// PeerCopy is one of our copies of a title, offered to a peer: its
// Jellyfin item and its audio languages.
type PeerCopy struct {
	Item  string
	Audio []string
}

// SetPeerVersions keeps what a peer's signed hello listed of MSFP's
// versions (Peer.Versions).
func (s *Store) SetPeerVersions(ctx context.Context, peerID, versions string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET versions = ? WHERE id = ?`, versions, peerID)
	return err
}

// PeerCopies lists our copies of a film or an episode in the libraries we
// offer a peer; none when the title isn't offered it.
func (s *Store) PeerCopies(ctx context.Context, peerID, key string) ([]PeerCopy, error) {
	// An episode a file holds after its first is that file's item
	// (range_of): what plays.
	rows, err := s.read.QueryContext(ctx, `SELECT coalesce(i.range_of, i.id) AS item, min(i.audio_tags) FROM catalog_items i
		JOIN peer_offers o ON o.peer_id = ? AND o.library_id = i.library_id
		WHERE i.title_key = ? AND i.kind IN ('movie', 'episode') GROUP BY item ORDER BY item`, peerID, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PeerCopy
	for rows.Next() {
		var c PeerCopy
		var audio sql.NullString
		if err := rows.Scan(&c.Item, &audio); err != nil {
			return nil, err
		}
		c.Audio = mergeTags(nil, audio.String)
		out = append(out, c)
	}
	return out, rows.Err()
}

// PeerSource is a paired server holding a title for us, with its copies'
// languages.
type PeerSource struct {
	Peer      *Peer
	Audio     []string
	Subtitles []string
}

// PeerSources lists the paired servers holding a film or an episode for
// us, in the order they were paired.
func (s *Store) PeerSources(ctx context.Context, key string) ([]PeerSource, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT t.peer_id, t.audio, t.subtitles FROM peer_titles t
		JOIN peers p ON p.id = t.peer_id AND p.state = 'paired' AND p.suspended_at IS NULL
		WHERE t.title_key = ? AND t.kind IN ('movie', 'episode') ORDER BY p.paired_at, p.id`, key)
	if err != nil {
		return nil, err
	}
	type row struct{ peer, audio, subs string }
	var found []row
	for rows.Next() {
		var r row
		var audio, subs sql.NullString
		if err := rows.Scan(&r.peer, &audio, &subs); err != nil {
			rows.Close()
			return nil, err
		}
		r.audio, r.subs = audio.String, subs.String
		found = append(found, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []PeerSource
	for _, r := range found {
		p, err := s.PeerByID(ctx, r.peer)
		if err != nil {
			continue
		}
		out = append(out, PeerSource{Peer: p, Audio: mergeTags(nil, r.audio), Subtitles: mergeTags(nil, r.subs)})
	}
	return out, nil
}

// ItemAudio is each item's audio languages, as tags.
func (s *Store) ItemAudio(ctx context.Context, items []string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, id := range items {
		var audio sql.NullString
		err := s.read.QueryRowContext(ctx, `SELECT audio_tags FROM catalog_items WHERE id = ?`, id).Scan(&audio)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		out[id] = mergeTags(nil, audio.String)
	}
	return out, nil
}

// TitlePeers are the paired servers holding titles for us, by key, as
// TitleInfos gives them (Peers), for what needs nothing else of them.
func (s *Store) TitlePeers(ctx context.Context, keys []string) (map[string][]string, error) {
	out := map[string][]string{}
	if keys = plainKeys(keys); len(keys) == 0 {
		return out, nil
	}
	rows, err := s.read.QueryContext(ctx, `SELECT t.title_key, t.peer_id FROM peer_titles t
		JOIN (SELECT DISTINCT value FROM json_each(?)) j ON j.value = t.title_key
		JOIN peers p ON p.id = t.peer_id AND p.state = 'paired' AND p.suspended_at IS NULL ORDER BY t.title_key, p.paired_at, p.id`, jsonList(keys))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, id string
		if err := rows.Scan(&key, &id); err != nil {
			return nil, err
		}
		out[key] = append(out[key], id)
	}
	return out, rows.Err()
}

// LivePairingCodes reports whether a pairing code of ours is still live:
// only then may a server this one doesn't know be heard (to pair).
func (s *Store) LivePairingCodes(ctx context.Context) (bool, error) {
	var live bool
	err := s.read.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pairing_codes WHERE expires_at > ?)`, s.now().Unix()).Scan(&live)
	return live, err
}

// changesHead is SQL for the number of the last change noted in the
// catalog (title_changes): 0 before the first. It holds once old changes
// are trimmed.
const changesHead = `SELECT coalesce((SELECT seq FROM sqlite_sequence WHERE name = 'title_changes'), 0)`

// TitleChanges says where the catalog's changes stand: head, the last
// noted; floor, the last one no longer kept (a peer that read up to floor
// or later can be told what changed since).
func (s *Store) TitleChanges(ctx context.Context) (head, floor int64, err error) {
	err = s.read.QueryRowContext(ctx, `SELECT (`+changesHead+`), coalesce((SELECT min(seq) - 1 FROM title_changes), (`+changesHead+`))`).
		Scan(&head, &floor)
	return head, floor, err
}

// PeerOffersSeq is the change the catalog was at when what we offer a
// peer last changed.
func (s *Store) PeerOffersSeq(ctx context.Context, peerID string) (int64, error) {
	var seq int64
	err := s.read.QueryRowContext(ctx, `SELECT offers_seq FROM peers WHERE id = ?`, peerID).Scan(&seq)
	return seq, err
}

// ChangedTitles lists, in key order after after, at most limit of the
// title keys changed since change since, as the libraries hold them now:
// present, those they still hold; gone, those they don't and that peer
// was told of (peer_sent: never one it couldn't know). last is the last
// key looked at ("" when none).
// more says limit keys were looked at: there may be more after last.
func (s *Store) ChangedTitles(ctx context.Context, peerID string, libraries []string, since int64, after string, limit int) (present []PeerTitle, gone []string, last string, more bool, err error) {
	rows, err := s.read.QueryContext(ctx, `SELECT DISTINCT title_key FROM title_changes
		WHERE seq > ? AND title_key > ? AND title_key NOT LIKE 'item/%' ORDER BY title_key LIMIT ?`, since, after, limit)
	if err != nil {
		return nil, nil, "", false, err
	}
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return nil, nil, "", false, err
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, "", false, err
	}
	if len(keys) == 0 {
		return []PeerTitle{}, nil, "", false, nil
	}
	present = []PeerTitle{}
	if len(libraries) > 0 {
		if present, _, err = s.titlesIn(ctx, libraries, keys, "", len(keys)); err != nil {
			return nil, nil, "", false, err
		}
	}
	held := map[string]bool{}
	for _, t := range present {
		held[t.Key] = true
	}
	for _, k := range keys {
		if held[k] {
			continue
		}
		var told int
		if err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM peer_sent WHERE peer_id = ? AND title_key = ?`, peerID, k).Scan(&told); err != nil {
			return nil, nil, "", false, err
		}
		if told > 0 {
			gone = append(gone, k)
		}
	}
	return present, gone, keys[len(keys)-1], len(keys) == limit, nil
}

// TrimTitleChanges forgets the changes noted before a time: a peer that
// last read before them gets the whole list again.
func (s *Store) TrimTitleChanges(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM title_changes WHERE at < ?`, before.Unix())
	return err
}

// PeerTitlesRead says up to which change of a peer's catalog we read its
// titles (0: none yet, or what we want of it changed since), and the
// generation of what we want of it.
func (s *Store) PeerTitlesRead(ctx context.Context, peerID string) (seq, wants int64, err error) {
	err = s.read.QueryRowContext(ctx, `SELECT titles_seq, wants_gen FROM peers WHERE id = ?`, peerID).Scan(&seq, &wants)
	return seq, wants, err
}

// SetPeerTitlesRead keeps up to which change of a peer's catalog we read
// its titles, unless what we want of it changed since the reading began
// (wants, its generation then): that reading no longer counts.
func (s *Store) SetPeerTitlesRead(ctx context.Context, peerID string, seq, wants int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET titles_seq = ?, titles_at = ? WHERE id = ? AND wants_gen = ?`,
		seq, s.now().Unix(), peerID, wants)
	return err
}

// DropPeerTitles forgets titles a peer no longer holds for us.
func (s *Store) DropPeerTitles(ctx context.Context, peerID string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM peer_titles WHERE peer_id = ? AND title_key IN (SELECT value FROM json_each(?))`,
		peerID, jsonList(keys))
	return err
}

// NewTitles are the titles the catalog changed after seq (come, gone or
// changed), at most limit, with the last change number read; head when
// since is negative: where the log stands now, nothing read.
func (s *Store) NewTitles(ctx context.Context, since int64, limit int) (keys []string, last int64, err error) {
	if since < 0 {
		head, _, err := s.TitleChanges(ctx)
		return nil, head, err
	}
	rows, err := s.read.QueryContext(ctx, `SELECT seq, title_key FROM title_changes
		WHERE seq > ? AND title_key NOT LIKE 'item/%' ORDER BY seq LIMIT ?`, since, limit)
	if err != nil {
		return nil, since, err
	}
	defer rows.Close()
	last = since
	seen := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&last, &k); err != nil {
			return nil, since, err
		}
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	return keys, last, rows.Err()
}

// PeerTold records what a peer was told it holds of ours (present). Those
// told gone stay recorded: the peer may not have kept that answer (its
// reading failed later), and asks again; telling it gone again tells it
// nothing it didn't know.
func (s *Store) PeerTold(ctx context.Context, peerID string, present []PeerTitle, gone []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range present {
		if _, err := tx.ExecContext(ctx, `INSERT INTO peer_sent (peer_id, title_key) VALUES (?, ?) ON CONFLICT DO NOTHING`, peerID, t.Key); err != nil {
			return err
		}
	}
	_ = gone
	return tx.Commit()
}

// ItemOfferedTo says whether one of our items (by Jellyfin ID) is in a
// library we offer the peer.
func (s *Store) ItemOfferedTo(ctx context.Context, peerID, itemID string) (bool, error) {
	var ok bool
	err := s.read.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM catalog_items i JOIN peer_offers o ON o.library_id = i.library_id
		WHERE i.id = ? AND o.peer_id = ?)`, itemID, peerID).Scan(&ok)
	return ok, err
}

// PublicLibraryIDs gives our libraries' IDs as paired servers know them
// (MSFP catalog, section 2.1), by our own (Jellyfin's) IDs: random,
// made the first time a library is asked for (first offered), then kept.
func (s *Store) PublicLibraryIDs(ctx context.Context, libraryIDs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(libraryIDs) == 0 {
		return out, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, id := range libraryIDs {
		public := randomPublicID()
		if _, err := tx.ExecContext(ctx, `INSERT INTO federation_libraries (library_id, public_id) VALUES (?, ?)
			ON CONFLICT (library_id) DO NOTHING`, id, public); err != nil {
			return nil, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT public_id FROM federation_libraries WHERE library_id = ?`, id).Scan(&public); err != nil {
			return nil, err
		}
		out[id] = public
	}
	return out, tx.Commit()
}

// randomPublicID is a library's public ID: 128 random bits in base64url,
// after "lib_".
func randomPublicID() string {
	b := make([]byte, 16)
	rand.Read(b) // crypto/rand.Read never fails on supported systems
	return "lib_" + base64.RawURLEncoding.EncodeToString(b)
}

// RemotePlayback is one of this server's playbacks on a paired server:
// that server's ID for it.
type RemotePlayback struct {
	PeerID, RemoteID string
}

// AddRemotePlayback remembers a playback started on a paired server.
func (s *Store) AddRemotePlayback(ctx context.Context, peerID, remoteID string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO remote_playbacks (peer_id, remote_id) VALUES (?, ?)`, peerID, remoteID)
	return err
}

// ForgetRemotePlayback forgets a playback on a paired server, over.
func (s *Store) ForgetRemotePlayback(ctx context.Context, peerID, remoteID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM remote_playbacks WHERE peer_id = ? AND remote_id = ?`, peerID, remoteID)
	return err
}

// TakeRemotePlaybacks lists the playbacks on paired servers remembered,
// and forgets them all.
func (s *Store) TakeRemotePlaybacks(ctx context.Context) ([]RemotePlayback, error) {
	rows, err := s.db.QueryContext(ctx, `DELETE FROM remote_playbacks RETURNING peer_id, remote_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RemotePlayback
	for rows.Next() {
		var p RemotePlayback
		if err := rows.Scan(&p.PeerID, &p.RemoteID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
