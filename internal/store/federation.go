package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
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
//
// Its key is remembered, by its hash (PeerForgotten): its requests are
// answered "unknown", signed.
func (s *Store) DeletePeer(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var key []byte
	switch err := tx.QueryRowContext(ctx, `SELECT public_key FROM peers WHERE id = ?`, id).Scan(&key); {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return err
	}
	hash := sha256.Sum256(key)
	if _, err := tx.ExecContext(ctx, `INSERT INTO forgotten_peers (key_hash, forgotten_at) VALUES (?, ?)
		ON CONFLICT (key_hash) DO UPDATE SET forgotten_at = excluded.forgotten_at`, hash[:], s.now().Unix()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM peers WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
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
	// A peer asking for what changed since before this gets the whole list:
	// the libraries' titles it gains or loses aren't changes of theirs.
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
}

// PeerWants lists the peer's libraries we want, by name.
func (s *Store) PeerWants(ctx context.Context, peerID string) ([]PeerLibrary, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT library_id, name, kind, coalesce(label, '') FROM peer_wants WHERE peer_id = ? ORDER BY name, library_id`, peerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PeerLibrary{}
	for rows.Next() {
		var l PeerLibrary
		if err := rows.Scan(&l.ID, &l.Name, &l.Kind, &l.Label); err != nil {
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO peer_wants (peer_id, library_id, name, kind, menu_id)
			VALUES (?, ?, ?, ?, lower(hex(randomblob(16))))
			ON CONFLICT (peer_id, library_id) DO UPDATE SET name = excluded.name, kind = excluded.kind`,
			peerID, l.ID, l.Name, l.Kind); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RenamePeerWants keeps what a peer now calls the libraries we want of it
// (those it still offers), and what it calls itself.
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
		FROM peer_wants w JOIN peers p ON p.id = w.peer_id AND p.state = 'paired'
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
}

// TitlesInLibraries lists, in key order after after, at most limit of the
// identified titles the given libraries hold (films, series, seasons,
// episodes; files TMDB doesn't know are left out), each once, with one of
// the libraries holding it.
func (s *Store) TitlesInLibraries(ctx context.Context, libraries []string, after string, limit int) ([]PeerTitle, error) {
	return s.titlesIn(ctx, libraries, nil, after, limit)
}

// titlesIn is TitlesInLibraries, of keys only when keys isn't nil.
func (s *Store) titlesIn(ctx context.Context, libraries, keys []string, after string, limit int) ([]PeerTitle, error) {
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
	only := ""
	if keys != nil {
		only = ` AND title_key IN (SELECT value FROM json_each(?))`
		args = append(args, jsonList(keys))
	}
	args = append(args, limit)
	rows, err := s.read.QueryContext(ctx, `SELECT title_key, min(kind), min(library_id) FROM catalog_items
		WHERE title_key > ? AND library_id IN (`+marks+`) AND kind IN ('movie', 'series', 'season', 'episode')
			AND title_key NOT LIKE 'item/%'`+only+`
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
			WHERE title_key = ? AND library_id IN (`+marks+`)`, append([]any{out[i].Key}, args[1:1+len(libraries)]...)...)
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
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO peer_titles (peer_id, title_key, kind, library_id, audio, subtitles, added_at, seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (peer_id, title_key) DO UPDATE SET kind = excluded.kind, library_id = excluded.library_id,
			audio = excluded.audio, subtitles = excluded.subtitles, seen = max(seen, excluded.seen)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := s.now().Unix()
	for _, t := range titles {
		if _, err := stmt.ExecContext(ctx, peerID, t.Key, t.Kind, t.Library, jsonList(t.Audio), jsonList(t.Subtitles), now, reading); err != nil {
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
		JOIN peers p ON p.id = t.peer_id AND p.state = 'paired'
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
		JOIN peers p ON p.id = t.peer_id AND p.state = 'paired' ORDER BY t.title_key, p.paired_at, p.id`, jsonList(keys))
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
// present, those they still hold; gone, those they don't. last is the
// last key listed ("" when none).
func (s *Store) ChangedTitles(ctx context.Context, libraries []string, since int64, after string, limit int) (present []PeerTitle, gone []string, last string, err error) {
	rows, err := s.read.QueryContext(ctx, `SELECT DISTINCT title_key FROM title_changes
		WHERE seq > ? AND title_key > ? AND title_key NOT LIKE 'item/%' ORDER BY title_key LIMIT ?`, since, after, limit)
	if err != nil {
		return nil, nil, "", err
	}
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return nil, nil, "", err
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, "", err
	}
	if len(keys) == 0 {
		return []PeerTitle{}, nil, "", nil
	}
	present = []PeerTitle{}
	if len(libraries) > 0 {
		if present, err = s.titlesIn(ctx, libraries, keys, "", len(keys)); err != nil {
			return nil, nil, "", err
		}
	}
	held := map[string]bool{}
	for _, t := range present {
		held[t.Key] = true
	}
	for _, k := range keys {
		if !held[k] {
			gone = append(gone, k)
		}
	}
	return present, gone, keys[len(keys)-1], nil
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
