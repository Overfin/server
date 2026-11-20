package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"
)

// TMDB IDs TMDB removed (migration 133): a duplicate entry merged into
// another, which answers for the title from then on. Overfin moves its
// titles to the new ID (catalog.Titles), and keeps the old one here: what
// still comes under the old key (a playback's progress, a page loaded
// before the move, a peer not read again yet) goes to the new one.

// goneCache is tmdb_gone's resolved rows, by the old ID's key: read on
// every write by title key, so kept in memory, and dropped at each change.
type goneCache struct {
	mu     sync.Mutex
	loaded bool
	to     map[string]string // "movie/tmdb/1628448" → "movie/tmdb/1363988"
}

// SetGone keeps that TMDB removed the title kind/tmdb old, and that now
// ("" while unknown) took over. A title whose ID was moved before to old
// follows to now. Nothing else moves: MoveGone does that.
func (s *Store) SetGone(ctx context.Context, kind, old, now string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setGone(ctx, tx, kind, old, now, at); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.dropGone()
	return nil
}

// MoveGone moves everything kept under a title TMDB removed (kind/tmdb old)
// to the one that took over (now), the same film or series under a new
// ID, and keeps that it moved: what people did and asked of it (moveKeys,
// and wishes, requests, "not interested"), an admin's texts and artwork
// (title_edits), its arrivals and departures. The library's items move
// through SetMatch and SetOverride, before.
func (s *Store) MoveGone(ctx context.Context, kind, old, now string, at time.Time) error {
	if now == "" || now == old {
		return s.SetGone(ctx, kind, old, now, at)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	oldKey, newKey := kind+"/tmdb/"+old, kind+"/tmdb/"+now
	// The new series' episodes may have other IDs: moved by their numbers,
	// then named by the new IDs where they're known (as retitle does).
	if kind == "series" {
		if err := marksToNumbers(ctx, tx, old); err != nil {
			return err
		}
	}
	if err := moveKeys(ctx, tx, oldKey, newKey); err != nil {
		return err
	}
	if kind == "series" {
		if err := marksToIDs(ctx, tx, now, -1); err != nil {
			return err
		}
	}
	if err := moveIn(ctx, tx, titleTables, oldKey, newKey); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE OR IGNORE title_edits SET tmdb = ? WHERE kind = ? AND tmdb = ?`, now, kind, old); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM title_edits WHERE kind = ? AND tmdb = ?`, kind, old); err != nil {
		return err
	}
	historyKinds := map[string]string{"movie": "movie", "series": "season"}
	if _, err := tx.ExecContext(ctx, `UPDATE title_history SET tmdb = ? WHERE kind = ? AND tmdb = ?`, now, historyKinds[kind], old); err != nil {
		return err
	}
	if err := setGone(ctx, tx, kind, old, now, at); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.dropGone()
	return nil
}

func (s *Store) dropGone() {
	s.gone.mu.Lock()
	s.gone.loaded = false
	s.gone.mu.Unlock()
}

func setGone(ctx context.Context, tx *sql.Tx, kind, old, now string, at time.Time) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO tmdb_gone (kind, old, new, at) VALUES (?, ?, ?, ?)
		ON CONFLICT (kind, old) DO UPDATE SET new = excluded.new, at = excluded.at`, kind, old, now, at.Unix()); err != nil {
		return err
	}
	if now != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE tmdb_gone SET new = ? WHERE kind = ? AND new = ?`, now, kind, old); err != nil {
			return err
		}
		// A title that comes back is no longer gone.
		if _, err := tx.ExecContext(ctx, `DELETE FROM tmdb_gone WHERE kind = ? AND old = ?`, kind, now); err != nil {
			return err
		}
	}
	return nil
}

// GoneOf says whether TMDB removed kind/tmdb old, and the ID that took
// over ("" while unknown).
func (s *Store) GoneOf(ctx context.Context, kind, old string) (now string, gone bool, err error) {
	err = s.read.QueryRowContext(ctx, `SELECT new FROM tmdb_gone WHERE kind = ? AND old = ?`, kind, old).Scan(&now)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return now, err == nil, err
}

// Gone is every TMDB ID TMDB removed, by kind and ID ("movie/1628448"),
// and the ID that took over ("" while unknown).
func (s *Store) Gone(ctx context.Context) (map[string]string, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT kind, old, new FROM tmdb_gone`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var kind, old, now string
		if err := rows.Scan(&kind, &old, &now); err != nil {
			return nil, err
		}
		out[kind+"/"+old] = now
	}
	return out, rows.Err()
}

// CurrentKey is the key a title is kept under now: a key under a TMDB ID
// TMDB removed, its seasons' and episodes' too, goes to the ID that took
// over; any other key is returned as it is.
func (s *Store) CurrentKey(ctx context.Context, key string) string {
	parts := strings.SplitN(key, "/", 4)
	if len(parts) < 3 || parts[1] != "tmdb" || (parts[0] != "movie" && parts[0] != "series") {
		return key
	}
	base := parts[0] + "/tmdb/" + parts[2]
	to, ok := s.goneKeys(ctx)[base]
	if !ok {
		return key
	}
	// An episode by its ID: the new series' episodes may have others, so
	// it goes by its numbers.
	if r, ok := ParseTitleKey(key); ok && r.Kind == "episode" && r.EpisodeID > 0 {
		var season, episode int
		if err := s.read.QueryRowContext(ctx, `SELECT season, episode FROM own_episodes WHERE series = ? AND tmdb_id = ?
			ORDER BY season = 0, season, episode LIMIT 1`, r.ID, r.EpisodeID).Scan(&season, &episode); err == nil {
			n, _ := ParseTitleKey(to)
			k, err := s.ownEpisodeKey(ctx, TitleRef{Kind: "episode", Source: "tmdb", ID: n.ID, Season: season, Episode: episode})
			if err == nil {
				return k
			}
		}
	}
	return to + key[len(base):]
}

// goneKeys is goneCache's map, read from the database when dropped. A
// failed read gives none: the keys stay as they are.
func (s *Store) goneKeys(ctx context.Context) map[string]string {
	s.gone.mu.Lock()
	defer s.gone.mu.Unlock()
	if s.gone.loaded {
		return s.gone.to
	}
	rows, err := s.read.QueryContext(ctx, `SELECT kind, old, new FROM tmdb_gone WHERE new != ''`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	to := map[string]string{}
	for rows.Next() {
		var kind, old, now string
		if err := rows.Scan(&kind, &old, &now); err != nil {
			return nil
		}
		to[kind+"/tmdb/"+old] = kind + "/tmdb/" + now
	}
	if rows.Err() != nil {
		return nil
	}
	s.gone.to, s.gone.loaded = to, true
	return to
}

// currentKeyMap is titles (title key → item ID) with each key current
// (CurrentKey).
func (s *Store) currentKeyMap(ctx context.Context, titles map[string]string) map[string]string {
	out := make(map[string]string, len(titles))
	for k, v := range titles {
		out[s.CurrentKey(ctx, k)] = v
	}
	return out
}

// currentKeyList is titles with each key current (CurrentKey), in order.
func (s *Store) currentKeyList(ctx context.Context, titles []string) []string {
	out := make([]string, len(titles))
	for i, k := range titles {
		out[i] = s.CurrentKey(ctx, k)
	}
	return out
}

// GoneItems are the library's films and series under the TMDB title
// kind/tmdb: to move when TMDB removed it.
func (s *Store) GoneItems(ctx context.Context, kind, tmdb string) ([]MatchTarget, error) {
	return s.matchTargets(ctx, `WHERE i.kind = ? AND i.tmdb = ? ORDER BY i.id`, kind, tmdb)
}

// RefetchIMDbConflicts marks for a fetch soon the library's films and
// series whose IMDb ID a paired server's title has under another TMDB ID:
// the library's may be under an ID TMDB removed, merged into the other's,
// and its page fetched again tells (catalog.Titles retires it). One
// fetched since before (a day ago) only, for two TMDB titles that do
// share an IMDb ID; it says how many. All the paired servers' titles are
// looked at, those read before as well: a reading of changes only brings
// what changed.
func (s *Store) RefetchIMDbConflicts(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM artwork_fetched WHERE fetched_at < ? AND key IN (
			SELECT i.kind || '/tmdb/' || i.tmdb FROM peer_titles p
			JOIN catalog_items i ON i.imdb = json_extract(p.ids, '$.imdb') AND i.kind = p.kind
			WHERE p.kind IN ('movie', 'series') AND p.ids IS NOT NULL
				AND p.title_key LIKE p.kind || '/tmdb/%' AND i.tmdb IS NOT NULL
				AND p.title_key != i.kind || '/tmdb/' || i.tmdb)`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
