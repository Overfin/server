package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// FrameCandidate is a library episode that may need a frame: its title,
// one of its files, and that file's length (0 when unknown).
type FrameCandidate struct {
	TitleKey string
	Item     string
	Runtime  time.Duration
}

// FrameCandidates lists the library's episodes without a frame, and those
// whose last try found none before retryAfter: one file each, the first by
// ID. Whether TMDB has a picture for them is the caller's to check.
func (s *Store) FrameCandidates(ctx context.Context, retryBefore time.Time, limit int) ([]FrameCandidate, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT c.title_key, min(c.id), coalesce(max(c.runtime_ms), 0)
		FROM catalog_items c LEFT JOIN episode_frames f ON f.title_key = c.title_key
		WHERE c.kind = 'episode' AND c.title_key IS NOT NULL
		  AND (f.title_key IS NULL OR (f.data IS NULL AND f.tried_at < ?))
		GROUP BY c.title_key ORDER BY c.title_key LIMIT ?`, retryBefore.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FrameCandidate
	for rows.Next() {
		var c FrameCandidate
		var ms int64
		if err := rows.Scan(&c.TitleKey, &c.Item, &ms); err != nil {
			return nil, err
		}
		c.Runtime = time.Duration(ms) * time.Millisecond
		out = append(out, c)
	}
	return out, rows.Err()
}

// PutFrame keeps an episode's frame (Overfin's own JPEG) under a new id,
// replacing any before; data nil notes a try that found none.
func (s *Store) PutFrame(ctx context.Context, titleKey, item, id string, data []byte, at time.Time) error {
	if data != nil && (len(id) != 32 || !allOf(id, isLowerHex)) {
		return errors.New("store: bad frame ID")
	}
	var nullID any
	if data != nil {
		nullID = id
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO episode_frames (title_key, item, id, data, tries, tried_at)
		VALUES (?1, ?2, ?3, ?4, 1, ?5)
		ON CONFLICT (title_key) DO UPDATE SET item = ?2, id = ?3, data = ?4, tries = tries + 1, tried_at = ?5`,
		titleKey, item, nullID, data, at.Unix())
	return err
}

// Frame returns a frame and its episode's title key; false when there's
// none by that id.
func (s *Store) Frame(ctx context.Context, id string) (data []byte, titleKey string, ok bool, err error) {
	err = s.read.QueryRowContext(ctx, `SELECT data, title_key FROM episode_frames WHERE id = ? AND data IS NOT NULL`, id).
		Scan(&data, &titleKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", false, nil
	}
	return data, titleKey, err == nil, err
}

// frameOf is an episode's frame ID, "" when it has none.
func (s *Store) frameOf(ctx context.Context, titleKey string) (string, error) {
	var id sql.NullString
	err := s.read.QueryRowContext(ctx, `SELECT id FROM episode_frames WHERE title_key = ? AND data IS NOT NULL`, titleKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id.String, err
}

// SkipFrame notes an episode that needs no frame (TMDB has a picture for
// it), so it's not looked at again before `until`.
func (s *Store) SkipFrame(ctx context.Context, titleKey, item string, until time.Time, retry time.Duration) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO episode_frames (title_key, item, tried_at) VALUES (?1, ?2, ?3)
		ON CONFLICT (title_key) DO UPDATE SET item = ?2, tried_at = ?3 WHERE data IS NULL`,
		titleKey, item, until.Add(-retry).Unix())
	return err
}
