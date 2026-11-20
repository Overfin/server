package store

import (
	"context"
	"time"
)

// SetNotInterested records that a profile isn't interested in a film or a
// series (on), or takes that back.
func (s *Store) SetNotInterested(ctx context.Context, profileID, title string, on bool, at time.Time) error {
	title = s.CurrentKey(ctx, title)
	if !on {
		_, err := s.db.ExecContext(ctx, `DELETE FROM not_interested WHERE profile_id = ? AND title = ?`, profileID, title)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO not_interested (profile_id, title, at) VALUES (?, ?, ?)
		ON CONFLICT (profile_id, title) DO NOTHING`, profileID, title, at.Unix())
	return err
}

// NotInterested are the titles a profile isn't interested in, by key.
func (s *Store) NotInterested(ctx context.Context, profileID string) (map[string]bool, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT title FROM not_interested WHERE profile_id = ?`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// IsNotInterested says whether a profile said it isn't interested in a
// title.
func (s *Store) IsNotInterested(ctx context.Context, profileID, title string) (bool, error) {
	title = s.CurrentKey(ctx, title)
	var n int
	err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM not_interested WHERE profile_id = ? AND title = ?`, profileID, title).Scan(&n)
	return n > 0, err
}
