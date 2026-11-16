package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// PreviewFile is a Jellyfin version's fingerprint, as taken when the file
// had that size and modification time.
type PreviewFile struct {
	ItemID, SourceID string
	Size, Modified   int64
	Fingerprint      string
}

// PreviewFileOf is a version's fingerprint, as last taken; ok is false
// when none was.
func (s *Store) PreviewFileOf(ctx context.Context, itemID, sourceID string) (f PreviewFile, ok bool, err error) {
	err = s.read.QueryRowContext(ctx, `SELECT size, modified, fingerprint FROM preview_files WHERE item_id = ? AND source_id = ?`,
		itemID, sourceID).Scan(&f.Size, &f.Modified, &f.Fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return PreviewFile{}, false, nil
	}
	f.ItemID, f.SourceID = itemID, sourceID
	return f, err == nil, err
}

// SetPreviewFile keeps a version's fingerprint, seen now.
func (s *Store) SetPreviewFile(ctx context.Context, f PreviewFile, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO preview_files (item_id, source_id, size, modified, fingerprint, seen_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (item_id, source_id) DO UPDATE SET
		size = excluded.size, modified = excluded.modified, fingerprint = excluded.fingerprint, seen_at = excluded.seen_at`,
		f.ItemID, f.SourceID, f.Size, f.Modified, f.Fingerprint, now.Unix())
	return err
}

// ForgetPreviewFiles forgets the versions not in seen (item ID, then
// source ID): their files are gone from the libraries.
func (s *Store) ForgetPreviewFiles(ctx context.Context, seen map[[2]string]bool) error {
	rows, err := s.read.QueryContext(ctx, `SELECT item_id, source_id FROM preview_files`)
	if err != nil {
		return err
	}
	var gone [][2]string
	for rows.Next() {
		var k [2]string
		if err := rows.Scan(&k[0], &k[1]); err != nil {
			rows.Close()
			return err
		}
		if !seen[k] {
			gone = append(gone, k)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, k := range gone {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM preview_files WHERE item_id = ? AND source_id = ?`, k[0], k[1]); err != nil {
			return err
		}
	}
	return nil
}

// PreviewFingerprints are the fingerprints of the libraries' files.
func (s *Store) PreviewFingerprints(ctx context.Context) (map[string]bool, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT DISTINCT fingerprint FROM preview_files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var fp string
		if err := rows.Scan(&fp); err != nil {
			return nil, err
		}
		out[fp] = true
	}
	return out, rows.Err()
}

// UnusedPreviews marks the previews no file has (unused) since now, those
// not marked yet, unmarks the others, and returns those unused since
// before cutoff: to delete.
func (s *Store) UnusedPreviews(ctx context.Context, unused []string, now, cutoff time.Time) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS unused_now (fingerprint TEXT PRIMARY KEY)`); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM unused_now`); err != nil {
		return nil, err
	}
	for _, fp := range unused {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO unused_now VALUES (?)`, fp); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM previews_unused WHERE fingerprint NOT IN (SELECT fingerprint FROM unused_now)`); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO previews_unused SELECT fingerprint, ? FROM unused_now`, now.Unix()); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT fingerprint FROM previews_unused WHERE since < ?`, cutoff.Unix())
	if err != nil {
		return nil, err
	}
	var gone []string
	for rows.Next() {
		var fp string
		if err := rows.Scan(&fp); err != nil {
			rows.Close()
			return nil, err
		}
		gone = append(gone, fp)
	}
	rows.Close()
	for _, fp := range gone {
		if _, err := tx.ExecContext(ctx, `DELETE FROM previews_unused WHERE fingerprint = ?`, fp); err != nil {
			return nil, err
		}
	}
	return gone, tx.Commit()
}

// PreviewFailed notes that making a fingerprint's previews failed now.
func (s *Store) PreviewFailed(ctx context.Context, fp string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO previews_failed (fingerprint, at) VALUES (?, ?)
		ON CONFLICT (fingerprint) DO UPDATE SET at = excluded.at`, fp, now.Unix())
	return err
}

// PreviewsFailedSince are the fingerprints whose previews failed since.
func (s *Store) PreviewsFailedSince(ctx context.Context, since time.Time) (map[string]bool, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT fingerprint FROM previews_failed WHERE at >= ?`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var fp string
		if err := rows.Scan(&fp); err != nil {
			return nil, err
		}
		out[fp] = true
	}
	return out, rows.Err()
}

// ItemRanks are the libraries' items' ranks on the server (title_ranks), a
// film's own, an episode's its series': the order previews are made in.
func (s *Store) ItemRanks(ctx context.Context) (map[string]int, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT c.id, coalesce(r.rank, sr.rank) FROM catalog_items c
		LEFT JOIN title_ranks r ON r.title_key = c.title_key
		LEFT JOIN catalog_items s ON s.id = c.series_id
		LEFT JOIN title_ranks sr ON sr.title_key = s.title_key
		WHERE c.kind IN ('movie', 'episode') AND coalesce(r.rank, sr.rank) IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var rank int
		if err := rows.Scan(&id, &rank); err != nil {
			return nil, err
		}
		out[id] = rank
	}
	return out, rows.Err()
}
