package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	// Limits is what it may use of this server at once; Theirs, what it
	// says we may use of it (nil until it said).
	Limits PeerLimits  `json:"limits"`
	Theirs *PeerLimits `json:"theirs,omitempty"`
}

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

const peerSelect = `SELECT id, public_key, address, name, state, created_at, paired_at, seen_at,
	max_streams, max_transcodes, max_bitrate_kbps, their_streams, their_transcodes, their_bitrate_kbps FROM peers`

func scanPeer(row interface{ Scan(...any) error }) (*Peer, error) {
	var p Peer
	var created int64
	var paired, seen, ts, tt, tb sql.NullInt64
	if err := row.Scan(&p.ID, &p.PublicKey, &p.Address, &p.Name, &p.State, &created, &paired, &seen,
		&p.Limits.Streams, &p.Limits.Transcodes, &p.Limits.BitrateKbps, &ts, &tt, &tb); err != nil {
		return nil, err
	}
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

// heldTitles, in SQL, are the films and series this server can play or
// will: its library's, and those its paired servers hold for it (by TMDB
// ID), as (kind, tmdb) rows. TMDB's data is kept for all of them alike.
const heldTitles = `SELECT kind, tmdb FROM catalog_items WHERE kind IN ('movie', 'series') AND tmdb IS NOT NULL
	UNION SELECT p.kind, substr(p.title_key, length(p.kind) + 7) FROM peer_titles p
		WHERE p.kind IN ('movie', 'series') AND p.title_key LIKE p.kind || '/tmdb/%'
		AND EXISTS (SELECT 1 FROM peers e WHERE e.id = p.peer_id AND e.state = 'paired')`

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
}

// TitlesInLibraries lists, in key order after after, at most limit of the
// identified titles the given libraries hold (films, series, seasons,
// episodes; files TMDB doesn't know are left out), each once, with one of
// the libraries holding it.
func (s *Store) TitlesInLibraries(ctx context.Context, libraries []string, after string, limit int) ([]PeerTitle, error) {
	out := []PeerTitle{}
	if len(libraries) == 0 {
		return out, nil
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
	args = append(args, limit)
	rows, err := s.read.QueryContext(ctx, `SELECT title_key, min(kind), min(library_id) FROM catalog_items
		WHERE title_key > ? AND library_id IN (`+marks+`) AND kind IN ('movie', 'series', 'season', 'episode')
			AND title_key NOT LIKE 'item/%'
		GROUP BY title_key ORDER BY title_key LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t PeerTitle
		if err := rows.Scan(&t.Key, &t.Kind, &t.Library); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Each one's languages: every copy's in those libraries.
	for i := range out {
		if out[i].Kind != "movie" && out[i].Kind != "episode" {
			continue
		}
		langs, err := s.read.QueryContext(ctx, `SELECT audio_tags, subtitle_tags FROM catalog_items
			WHERE title_key = ? AND library_id IN (`+marks+`)`, append([]any{out[i].Key}, args[1:len(args)-1]...)...)
		if err != nil {
			return nil, err
		}
		for langs.Next() {
			var audio, subs sql.NullString
			if err := langs.Scan(&audio, &subs); err != nil {
				langs.Close()
				return nil, err
			}
			out[i].Audio = mergeTags(out[i].Audio, audio.String)
			out[i].Subtitles = mergeTags(out[i].Subtitles, subs.String)
		}
		langs.Close()
		if err := langs.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
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

// SetPeerTitles replaces what we know a peer holds for us, and notes when.
func (s *Store) SetPeerTitles(ctx context.Context, peerID string, titles []PeerTitle) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM peer_titles WHERE peer_id = ?`, peerID); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO peer_titles (peer_id, title_key, kind, library_id, audio, subtitles)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, t := range titles {
		if _, err := stmt.ExecContext(ctx, peerID, t.Key, t.Kind, t.Library, jsonList(t.Audio), jsonList(t.Subtitles)); err != nil {
			return err
		}
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
	rows, err := s.read.QueryContext(ctx, `SELECT title_key, kind, library_id, audio, subtitles FROM peer_titles
		WHERE peer_id = ? ORDER BY title_key`, peerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PeerTitle{}
	for rows.Next() {
		var t PeerTitle
		var audio, subs sql.NullString
		if err := rows.Scan(&t.Key, &t.Kind, &t.Library, &audio, &subs); err != nil {
			return nil, err
		}
		t.Audio, t.Subtitles = mergeTags(nil, audio.String), mergeTags(nil, subs.String)
		out = append(out, t)
	}
	return out, rows.Err()
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

// PeerCopy is one of our copies of a title, offered to a peer: its
// Jellyfin item and its audio languages.
type PeerCopy struct {
	Item  string
	Audio []string
}

// PeerCopies lists our copies of a film or an episode in the libraries we
// offer a peer; none when the title isn't offered it.
func (s *Store) PeerCopies(ctx context.Context, peerID, key string) ([]PeerCopy, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT i.id, i.audio_tags FROM catalog_items i
		JOIN peer_offers o ON o.peer_id = ? AND o.library_id = i.library_id
		WHERE i.title_key = ? AND i.kind IN ('movie', 'episode') ORDER BY i.id`, peerID, key)
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
