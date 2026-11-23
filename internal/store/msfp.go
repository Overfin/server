package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/msfp-io/msfp-go"
)

// Federation's records, as the MSFP engine keeps them (msfp.Store): in
// this database, beside everything else, so that the site's pages join
// paired servers' titles with this server's own in SQL. The engine owns
// the meaning of every field; the store keeps them, in these tables:
//
//   - msfp_server: this server's record, JSON (its keys sealed);
//   - peers: each peer's record, JSON, with the columns the pages read
//     beside it (name, state, paired_at, suspended_at) and its current
//     key, copied from the record at each write;
//   - peer_offers: the libraries offered to each peer (kept apart: a
//     library's ID changes with its name, ReplaceLibraryID);
//   - peer_wants: the peer's libraries wanted, a copy of the record's,
//     with what this server adds (its menu entry's ID, the operator's
//     label);
//   - msfp_groups: each group's record, JSON (groups part);
//   - pairing_codes, forgotten_peers, federation_libraries, peer_sent,
//     remote_playbacks and peer_titles: as their names say.
//
// Times the engine compares (a forgotten key's, a code's) are kept in
// nanoseconds, so that they come back exactly as given.

// MSFP is the engine's store on this database.
func (s *Store) MSFP() msfp.Store { return msfpStore{s} }

type msfpStore struct{ s *Store }

// Read runs fn in a read-only transaction: one consistent view.
func (m msfpStore) Read(ctx context.Context, fn func(msfp.Tx) error) error {
	tx, err := m.s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return fn(&msfpTx{ctx: ctx, tx: tx, now: m.s.now})
}

// Update runs fn in a transaction, kept whole or not at all. The store
// has one connection for writes: two never interleave.
func (m msfpStore) Update(ctx context.Context, fn func(msfp.Tx) error) error {
	tx, err := m.s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(&msfpTx{ctx: ctx, tx: tx, now: m.s.now}); err != nil {
		return err
	}
	return tx.Commit()
}

// msfpTx is a transaction of the engine's.
type msfpTx struct {
	ctx context.Context
	tx  *sql.Tx
	now func() time.Time
}

func (t *msfpTx) exec(query string, args ...any) error {
	_, err := t.tx.ExecContext(t.ctx, query, args...)
	return err
}

// each runs fn on each row of query.
func (t *msfpTx) each(fn func(*sql.Rows) error, query string, args ...any) error {
	rows, err := t.tx.QueryContext(t.ctx, query, args...)
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

// nanos and fromNanos keep a time exactly: zero as 0.
func nanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func fromNanos(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

// ---- This server ------------------------------------------------------------

func (t *msfpTx) Server() (*msfp.ServerRecord, error) {
	var raw string
	err := t.tx.QueryRowContext(t.ctx, `SELECT record FROM msfp_server WHERE id = 1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r msfp.ServerRecord
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return nil, fmt.Errorf("store: this server's federation record: %w", err)
	}
	return &r, nil
}

func (t *msfpTx) PutServer(r msfp.ServerRecord) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return t.exec(`INSERT INTO msfp_server (id, record) VALUES (1, ?) ON CONFLICT (id) DO UPDATE SET record = excluded.record`, string(b))
}

// ---- Peers ------------------------------------------------------------------

// peerRecord reads a peer's record: its JSON, and its offers.
func (t *msfpTx) peerRecord(id, raw string) (*msfp.PeerRecord, error) {
	var p msfp.PeerRecord
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, fmt.Errorf("store: a peer's federation record: %w", err)
	}
	p.ID = id
	p.Offers = nil
	err := t.each(func(rows *sql.Rows) error {
		var l string
		if err := rows.Scan(&l); err != nil {
			return err
		}
		p.Offers = append(p.Offers, l)
		return nil
	}, `SELECT library_id FROM peer_offers WHERE peer_id = ? ORDER BY library_id`, id)
	return &p, err
}

func (t *msfpTx) Peers() ([]msfp.PeerRecord, error) {
	type row struct{ id, raw string }
	var rows []row
	err := t.each(func(r *sql.Rows) error {
		var x row
		if err := r.Scan(&x.id, &x.raw); err != nil {
			return err
		}
		rows = append(rows, x)
		return nil
	}, `SELECT id, record FROM peers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	out := make([]msfp.PeerRecord, 0, len(rows))
	for _, r := range rows {
		p, err := t.peerRecord(r.id, r.raw)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, nil
}

// onePeer reads the peer a query finds: nil for none.
func (t *msfpTx) onePeer(query string, arg any) (*msfp.PeerRecord, error) {
	var id, raw string
	err := t.tx.QueryRowContext(t.ctx, query, arg).Scan(&id, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return t.peerRecord(id, raw)
}

func (t *msfpTx) Peer(id string) (*msfp.PeerRecord, error) {
	return t.onePeer(`SELECT id, record FROM peers WHERE id = ?`, id)
}

func (t *msfpTx) PeerByKey(key []byte) (*msfp.PeerRecord, error) {
	return t.onePeer(`SELECT id, record FROM peers WHERE public_key = ?`, key)
}

// MenuID is the ID of a wanted library's entry in the site's menu: from
// the pairing and the library, so that a library wanted again (unticked,
// then ticked) gets the same one back, and categories that named it
// still do.
func MenuID(peerID, libraryID string) string {
	sum := sha256.Sum256([]byte(peerID + "\x00" + libraryID))
	return hex.EncodeToString(sum[:16])
}

func (t *msfpTx) PutPeer(p msfp.PeerRecord) error {
	offers := p.Offers
	p.Offers = nil
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	var paired, suspended any
	if !p.Paired.IsZero() {
		paired = p.Paired.Unix()
	}
	if !p.Suspended.IsZero() {
		suspended = p.Suspended.Unix()
	}
	if err := t.exec(`INSERT INTO peers (id, public_key, name, state, paired_at, suspended_at, record) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET public_key = excluded.public_key, name = excluded.name, state = excluded.state,
			paired_at = excluded.paired_at, suspended_at = excluded.suspended_at, record = excluded.record`,
		p.ID, p.Key, p.Name, string(p.State), paired, suspended, string(b)); err != nil {
		return err
	}
	// Its offers.
	if err := t.exec(`DELETE FROM peer_offers WHERE peer_id = ? AND library_id NOT IN (SELECT value FROM json_each(?))`,
		p.ID, jsonOrEmpty(offers)); err != nil {
		return err
	}
	for _, l := range offers {
		if err := t.exec(`INSERT OR IGNORE INTO peer_offers (peer_id, library_id) VALUES (?, ?)`, p.ID, l); err != nil {
			return err
		}
	}
	// The libraries wanted, for the site's menu and listings: those no
	// longer wanted go, with their titles at once (not at the next reading
	// of the peer's titles, which may not come soon); those still wanted
	// keep their entry (its ID, the operator's label).
	var wanted []string
	for _, w := range p.Wants {
		if w.Wanted {
			wanted = append(wanted, w.ID)
		}
	}
	if err := t.exec(`DELETE FROM peer_wants WHERE peer_id = ? AND library_id NOT IN (SELECT value FROM json_each(?))`,
		p.ID, jsonOrEmpty(wanted)); err != nil {
		return err
	}
	if err := t.exec(`DELETE FROM peer_titles WHERE peer_id = ? AND library_id NOT IN (SELECT value FROM json_each(?))`,
		p.ID, jsonOrEmpty(wanted)); err != nil {
		return err
	}
	for _, w := range p.Wants {
		if !w.Wanted {
			continue
		}
		// msfp-go's revised v0 sends no library kind (its titles' keys
		// tell it): a new row's kind is '' until the federation phase
		// (plan phase 7) types it by its first titles; a kept row keeps
		// the one it has.
		if err := t.exec(`INSERT INTO peer_wants (peer_id, library_id, name, kind, menu_id, offered) VALUES (?, ?, ?, '', ?, ?)
			ON CONFLICT (peer_id, library_id) DO UPDATE SET name = excluded.name, offered = excluded.offered`,
			p.ID, w.ID, w.Name, MenuID(p.ID, w.ID), !w.Gone); err != nil {
			return err
		}
	}
	return nil
}

// jsonOrEmpty is a list as JSON, [] for none: NOT IN an empty list holds
// for every row.
func jsonOrEmpty(l []string) string {
	if len(l) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(l)
	return string(b)
}

// DeletePeer removes a peer, and with it (by the tables' references) its
// offers, its wants, its titles, the titles it was sent and its
// leftovers.
func (t *msfpTx) DeletePeer(id string) error {
	return t.exec(`DELETE FROM peers WHERE id = ?`, id)
}

// ---- Codes ------------------------------------------------------------------

func (t *msfpTx) codes(where string, args ...any) ([]msfp.CodeRecord, error) {
	var out []msfp.CodeRecord
	err := t.each(func(rows *sql.Rows) error {
		var c msfp.CodeRecord
		var created, expires int64
		if err := rows.Scan(&c.ID, &c.Secret, &c.SecretHash, &c.Note, &created, &expires, &c.Uses, &c.Group); err != nil {
			return err
		}
		c.Created, c.Expires = fromNanos(created), fromNanos(expires)
		out = append(out, c)
		return nil
	}, `SELECT id, secret, secret_hash, note, created_at, expires_at, uses, group_id FROM pairing_codes`+where+` ORDER BY id`, args...)
	return out, err
}

func (t *msfpTx) Codes() ([]msfp.CodeRecord, error) { return t.codes("") }

func (t *msfpTx) CodeByHash(hash []byte) (*msfp.CodeRecord, error) {
	c, err := t.codes(` WHERE secret_hash = ?`, hash)
	if err != nil || len(c) == 0 {
		return nil, err
	}
	return &c[0], nil
}

func (t *msfpTx) PutCode(c msfp.CodeRecord) error {
	return t.exec(`INSERT INTO pairing_codes (id, secret_hash, secret, note, created_at, expires_at, uses, group_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET secret_hash = excluded.secret_hash, secret = excluded.secret, note = excluded.note,
			created_at = excluded.created_at, expires_at = excluded.expires_at, uses = excluded.uses, group_id = excluded.group_id`,
		c.ID, c.SecretHash, c.Secret, c.Note, nanos(c.Created), nanos(c.Expires), c.Uses, c.Group)
}

func (t *msfpTx) DeleteCode(id string) error {
	return t.exec(`DELETE FROM pairing_codes WHERE id = ?`, id)
}

// ---- Forgotten keys ---------------------------------------------------------

func (t *msfpTx) Forgotten(hash []byte) (*msfp.ForgottenKey, error) {
	var at int64
	var revoked bool
	err := t.tx.QueryRowContext(t.ctx, `SELECT forgotten_at, revoked FROM forgotten_peers WHERE key_hash = ?`, hash).Scan(&at, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &msfp.ForgottenKey{KeyHash: slices.Clone(hash), At: fromNanos(at), Revoked: revoked}, nil
}

func (t *msfpTx) Forget(f msfp.ForgottenKey) error {
	return t.exec(`INSERT INTO forgotten_peers (key_hash, forgotten_at, revoked) VALUES (?, ?, ?)
		ON CONFLICT (key_hash) DO UPDATE SET forgotten_at = excluded.forgotten_at, revoked = excluded.revoked`,
		f.KeyHash, nanos(f.At), f.Revoked)
}

func (t *msfpTx) Unforget(hash []byte) error {
	return t.exec(`DELETE FROM forgotten_peers WHERE key_hash = ?`, hash)
}

func (t *msfpTx) TrimForgotten(before time.Time) error {
	return t.exec(`DELETE FROM forgotten_peers WHERE forgotten_at < ?`, nanos(before))
}

// ---- Libraries, titles sent, leftovers --------------------------------------

func (t *msfpTx) LibraryIDs() (map[string]string, error) {
	out := map[string]string{}
	err := t.each(func(rows *sql.Rows) error {
		var host, public string
		if err := rows.Scan(&host, &public); err != nil {
			return err
		}
		out[host] = public
		return nil
	}, `SELECT library_id, public_id FROM federation_libraries`)
	return out, err
}

func (t *msfpTx) PutLibraryID(hostID, publicID string) error {
	return t.exec(`INSERT INTO federation_libraries (library_id, public_id) VALUES (?, ?)
		ON CONFLICT (library_id) DO UPDATE SET public_id = excluded.public_id`, hostID, publicID)
}

func (t *msfpTx) Sent(peer string, keys []string) ([]string, error) {
	var out []string
	if len(keys) == 0 {
		return nil, nil
	}
	err := t.each(func(rows *sql.Rows) error {
		var k string
		if err := rows.Scan(&k); err != nil {
			return err
		}
		out = append(out, k)
		return nil
	}, `SELECT title_key FROM peer_sent WHERE peer_id = ? AND title_key IN (SELECT value FROM json_each(?)) ORDER BY title_key`,
		peer, jsonOrEmpty(keys))
	return out, err
}

func (t *msfpTx) MarkSent(peer string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	return t.exec(`INSERT INTO peer_sent (peer_id, title_key) SELECT ?, value FROM json_each(?) WHERE true
		ON CONFLICT DO NOTHING`, peer, jsonOrEmpty(keys))
}

func (t *msfpTx) Leftovers() ([]msfp.Leftover, error) {
	var out []msfp.Leftover
	err := t.each(func(rows *sql.Rows) error {
		var l msfp.Leftover
		if err := rows.Scan(&l.Peer, &l.PlaybackID); err != nil {
			return err
		}
		out = append(out, l)
		return nil
	}, `SELECT peer_id, remote_id FROM remote_playbacks ORDER BY peer_id, remote_id`)
	return out, err
}

func (t *msfpTx) PutLeftover(l msfp.Leftover) error {
	return t.exec(`INSERT OR IGNORE INTO remote_playbacks (peer_id, remote_id) VALUES (?, ?)`, l.Peer, l.PlaybackID)
}

func (t *msfpTx) DeleteLeftover(l msfp.Leftover) error {
	return t.exec(`DELETE FROM remote_playbacks WHERE peer_id = ? AND remote_id = ?`, l.Peer, l.PlaybackID)
}

// ---- Peers' titles ----------------------------------------------------------

// Overfin reads films and series only: a peer's titles of another
// content part are refused (the engine never reads one, music being off
// here).
var errNotVideo = errors.New("store: only films and series are kept of a paired server")

func (t *msfpTx) PutPeerTitles(peer string, reading int64, titles []msfp.Title) error {
	if len(titles) == 0 {
		return nil
	}
	stmt, err := t.tx.PrepareContext(t.ctx, `INSERT INTO peer_titles (peer_id, title_key, kind, library_id, audio, subtitles, with_keys, ids, video, added_at, seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (peer_id, title_key) DO UPDATE SET kind = excluded.kind, library_id = excluded.library_id,
			audio = excluded.audio, subtitles = excluded.subtitles, with_keys = excluded.with_keys, ids = excluded.ids,
			video = excluded.video, seen = max(seen, excluded.seen)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	// When each arrived is kept: what the peer held already isn't new.
	now := t.now().Unix()
	for _, title := range titles {
		v, ok := title.(*msfp.VideoTitle)
		if !ok || len(v.Libraries) != 1 {
			return errNotVideo
		}
		var ids, video any
		if len(v.IDs) > 0 {
			b, _ := json.Marshal(v.IDs)
			ids = string(b)
		}
		// The title's languages and picture, from its copies (msfp-go's
		// revised v0 sends no summary); the copies themselves are kept
		// from the federation phase on (plan phase 7).
		if best, ok := v.BestCopy(); ok {
			if p, ok := copyPicture(best); ok {
				b, _ := json.Marshal(p)
				video = string(b)
			}
		}
		if _, err := stmt.ExecContext(t.ctx, peer, v.Key, v.Kind, v.Libraries[0], jsonList(v.AudioLanguages()), jsonList(v.SubtitleLanguages()),
			jsonList(v.With), ids, video, now, reading); err != nil {
			return err
		}
	}
	return nil
}

// EndReading drops the titles the reading didn't mark. Readings are
// numbered upwards (and one at a time for a peer): a title marked by a
// later one is never dropped.
func (t *msfpTx) EndReading(peer string, reading int64) error {
	if err := t.followPeerKeys(peer, `d.seen < ?`, `k.seen >= ?`, reading, reading); err != nil {
		return err
	}
	return t.exec(`DELETE FROM peer_titles WHERE peer_id = ? AND seen < ?`, peer, reading)
}

func (t *msfpTx) DropPeerTitles(peer string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	list := jsonOrEmpty(keys)
	if err := t.followPeerKeys(peer, `d.title_key IN (SELECT value FROM json_each(?))`,
		`k.title_key NOT IN (SELECT value FROM json_each(?))`, list, list); err != nil {
		return err
	}
	return t.exec(`DELETE FROM peer_titles WHERE peer_id = ? AND title_key IN (SELECT value FROM json_each(?))`, peer, list)
}

// followPeerKeys moves what profiles did with a peer's films and series
// about to go (dropped, of peer_titles d) to the peer's title of the same
// kind with the same IMDb ID that stays (kept, of peer_titles k): the
// peer's copy under a new key, TMDB having merged its entry into another.
// Its history follows, as for a library title (MoveGone); one that would
// go to two titles stays, and so does one the library or another peer
// still has (the library's is retired by its own refresh).
func (t *msfpTx) followPeerKeys(peer, dropped, kept string, droppedArg, keptArg any) error {
	type move struct{ from, to string }
	var moves []move
	err := t.each(func(rows *sql.Rows) error {
		var m move
		if err := rows.Scan(&m.from, &m.to); err != nil {
			return err
		}
		moves = append(moves, m)
		return nil
	}, `SELECT d.title_key, min(k.title_key) FROM peer_titles d
		JOIN peer_titles k ON k.peer_id = d.peer_id AND k.kind = d.kind AND k.title_key != d.title_key
			AND json_extract(k.ids, '$.imdb') = json_extract(d.ids, '$.imdb')
		WHERE d.peer_id = ? AND d.kind IN ('movie', 'series') AND d.ids IS NOT NULL AND `+dropped+` AND `+kept+`
			AND NOT EXISTS (SELECT 1 FROM catalog_items i WHERE i.title_key = d.title_key)
			AND NOT EXISTS (SELECT 1 FROM peer_titles o WHERE o.title_key = d.title_key AND o.peer_id != d.peer_id)
		GROUP BY d.title_key HAVING count(DISTINCT k.title_key) = 1`, peer, droppedArg, keptArg)
	if err != nil {
		return err
	}
	for _, m := range moves {
		if err := moveKeys(t.ctx, t.tx, m.from, m.to); err != nil {
			return err
		}
		if err := moveIn(t.ctx, t.tx, titleTables, m.from, m.to); err != nil {
			return err
		}
	}
	return nil
}

func (t *msfpTx) peerTitles(query string, args ...any) ([]msfp.Title, error) {
	var out []msfp.Title
	err := t.each(func(rows *sql.Rows) error {
		var v msfp.VideoTitle
		var library string
		var with, ids sql.NullString
		if err := rows.Scan(&v.Key, &v.Kind, &library, &with, &ids); err != nil {
			return err
		}
		// No copies: peer_titles keeps a summary only (languages,
		// picture) until the federation phase (plan phase 7) keeps
		// each title's copies.
		v.Libraries = []string{library}
		v.With = mergeTags(nil, with.String)
		if ids.Valid {
			if err := json.Unmarshal([]byte(ids.String), &v.IDs); err != nil {
				return err
			}
		}
		out = append(out, &v)
		return nil
	}, `SELECT title_key, kind, library_id, with_keys, ids FROM peer_titles `+query, args...)
	return out, err
}

func (t *msfpTx) PeerTitles(peer, after string, limit int) ([]msfp.Title, error) {
	return t.peerTitles(`WHERE peer_id = ? AND title_key > ? ORDER BY title_key LIMIT ?`, peer, after, limit)
}

func (t *msfpTx) PeerTitle(peer, key string) (msfp.Title, error) {
	titles, err := t.peerTitles(`WHERE peer_id = ? AND title_key = ?`, peer, key)
	if err != nil || len(titles) == 0 {
		return nil, err
	}
	return titles[0], nil
}

// ---- Groups (msfp.GroupsTx) ---------------------------------------------------

func (t *msfpTx) Groups() ([]msfp.GroupRecord, error) {
	var out []msfp.GroupRecord
	err := t.each(func(rows *sql.Rows) error {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		var g msfp.GroupRecord
		if err := json.Unmarshal([]byte(raw), &g); err != nil {
			return fmt.Errorf("store: a federation group's record: %w", err)
		}
		out = append(out, g)
		return nil
	}, `SELECT record FROM msfp_groups ORDER BY id`)
	return out, err
}

func (t *msfpTx) Group(id string) (*msfp.GroupRecord, error) {
	var raw string
	err := t.tx.QueryRowContext(t.ctx, `SELECT record FROM msfp_groups WHERE id = ?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var g msfp.GroupRecord
	if err := json.Unmarshal([]byte(raw), &g); err != nil {
		return nil, fmt.Errorf("store: a federation group's record: %w", err)
	}
	return &g, nil
}

func (t *msfpTx) PutGroup(g msfp.GroupRecord) error {
	b, err := json.Marshal(g)
	if err != nil {
		return err
	}
	return t.exec(`INSERT INTO msfp_groups (id, record) VALUES (?, ?) ON CONFLICT (id) DO UPDATE SET record = excluded.record`, g.ID, string(b))
}

func (t *msfpTx) DeleteGroup(id string) error {
	return t.exec(`DELETE FROM msfp_groups WHERE id = ?`, id)
}
