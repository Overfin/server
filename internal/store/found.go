package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Titles the library knows only by an IMDb or a TVDB ID, found on TMDB
// (migration 023).

// FindTarget is a film or a series to look for on TMDB by another ID.
type FindTarget struct {
	Kind     string // movie, series
	Source   string // imdb, tvdb
	External string
}

// TitlesToFind lists up to limit films and series without a TMDB ID that
// have an IMDb (else TVDB) ID not looked for since before.
func (s *Store) TitlesToFind(ctx context.Context, before time.Time, limit int) ([]FindTarget, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT DISTINCT i.kind, CASE WHEN i.imdb IS NOT NULL THEN 'imdb' ELSE 'tvdb' END,
			coalesce(i.imdb, i.tvdb)
		FROM catalog_items i
		WHERE i.kind IN ('movie', 'series') AND i.tmdb IS NULL AND (i.imdb IS NOT NULL OR i.tvdb IS NOT NULL)
		AND NOT EXISTS (SELECT 1 FROM tmdb_found f WHERE f.kind = i.kind
			AND f.source = CASE WHEN i.imdb IS NOT NULL THEN 'imdb' ELSE 'tvdb' END
			AND f.external = coalesce(i.imdb, i.tvdb) AND f.fetched_at >= ?)
		LIMIT ?`, before.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FindTarget
	for rows.Next() {
		var t FindTarget
		if err := rows.Scan(&t.Kind, &t.Source, &t.External); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetFound records what TMDB said a film's or a series' other ID names
// (tmdb "": nothing). Found, the index's items get the TMDB ID, and what
// profiles did with the title, and its requests, move to its new key.
func (s *Store) SetFound(ctx context.Context, t FindTarget, tmdb string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO tmdb_found (kind, source, external, tmdb, fetched_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (kind, source, external) DO UPDATE SET tmdb = excluded.tmdb, fetched_at = excluded.fetched_at`,
		t.Kind, t.Source, t.External, nullString(tmdb), at.Unix()); err != nil {
		return err
	}
	if tmdb != "" {
		column := map[string]string{"imdb": "imdb", "tvdb": "tvdb"}[t.Source]
		if _, err := tx.ExecContext(ctx, `UPDATE catalog_items SET tmdb = ? WHERE kind = ? AND tmdb IS NULL AND `+column+` = ?`,
			tmdb, t.Kind, t.External); err != nil {
			return err
		}
		// The title's key, and its seasons' and episodes', change with it.
		old, now := t.Kind+"/"+t.Source+"/"+t.External, t.Kind+"/tmdb/"+tmdb
		for _, table := range []string{"profile_titles", "requests"} {
			if _, err := tx.ExecContext(ctx, `UPDATE OR IGNORE `+table+` SET title = ? || substr(title, ?)
				WHERE title = ? OR title LIKE ? ESCAPE '\'`, now, len(old)+1, old, likePrefix(old+"/")); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.RefreshTitleKeys(ctx)
}

// IMDbTMDB says which TMDB film or series (kind) an IMDb ID names, for a
// list made of IMDb's votes, without asking TMDB: a title whose page gave
// that ID, else the one TMDB found for it, as long as its data was kept
// since fresh. "" when TMDB found none since noneSince. ok is false when
// TMDB is to be asked (FindTitle, then PutFound).
func (s *Store) IMDbTMDB(ctx context.Context, kind, imdb string, fresh, noneSince time.Time) (tmdb string, ok bool, err error) {
	err = s.read.QueryRowContext(ctx, `SELECT tmdb FROM catalog_titles WHERE kind = ? AND imdb = ? AND seen_at >= ? LIMIT 1`,
		kind, imdb, fresh.Unix()).Scan(&tmdb)
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return tmdb, err == nil, err
	}
	var found sql.NullString
	var at int64
	err = s.read.QueryRowContext(ctx, `SELECT tmdb, fetched_at FROM tmdb_found WHERE kind = ? AND source = 'imdb' AND external = ?`,
		kind, imdb).Scan(&found, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !found.Valid {
		return "", at >= noneSince.Unix(), nil
	}
	var kept bool
	err = s.read.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM catalog_titles WHERE kind = ? AND tmdb = ? AND seen_at >= ?)`,
		kind, found.String, fresh.Unix()).Scan(&kept)
	return found.String, kept && err == nil, err
}

// PutFound records what TMDB said an IMDb ID names (tmdb "": nothing), for
// a title no library has: unlike SetFound, the index stays as it is.
func (s *Store) PutFound(ctx context.Context, kind, imdb, tmdb string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO tmdb_found (kind, source, external, tmdb, fetched_at) VALUES (?, 'imdb', ?, ?, ?)
		ON CONFLICT (kind, source, external) DO UPDATE SET tmdb = excluded.tmdb, fetched_at = excluded.fetched_at`,
		kind, imdb, nullString(tmdb), at.Unix())
	return err
}

// foundTMDB is the TMDB ID an item is known by: the one an admin chose
// (title_overrides), else the one Overfin matched it to (title_matches),
// else Jellyfin's, else the one found for its other IDs; "" when there's
// none.
func (s *Store) foundTMDB(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, it CatalogItem) string {
	if it.Kind != "movie" && it.Kind != "series" {
		return it.TMDB
	}
	var tmdb sql.NullString
	if q.QueryRowContext(ctx, `SELECT tmdb FROM title_overrides WHERE item_id = ?`, it.ID).Scan(&tmdb); tmdb.Valid {
		return tmdb.String
	}
	if q.QueryRowContext(ctx, `SELECT tmdb FROM title_matches WHERE item_id = ?`, it.ID).Scan(&tmdb); tmdb.Valid {
		return tmdb.String
	}
	if it.TMDB != "" {
		return it.TMDB
	}
	q.QueryRowContext(ctx, `SELECT tmdb FROM tmdb_found WHERE kind = ? AND tmdb IS NOT NULL
		AND ((source = 'imdb' AND external = ?) OR (source = 'tvdb' AND external = ?)) LIMIT 1`,
		it.Kind, it.IMDb, it.TVDB).Scan(&tmdb)
	return tmdb.String
}
