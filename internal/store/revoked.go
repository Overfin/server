package store

import "context"

// Revoked is a login ended before its time: its cookie's SHA-256, when it
// would have expired, and its number.
type Revoked struct {
	Seq     int64
	Hash    []byte
	Expires int64
}

// RevokedLogins are the logins ended before their time since seq, still
// short of when they'd have expired, oldest first, at most limit.
func (s *Store) RevokedLogins(ctx context.Context, since int64, limit int) ([]Revoked, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT seq, token_hash, expires_at FROM revoked_logins
		WHERE seq > ? AND expires_at > ? ORDER BY seq LIMIT ?`, since, s.now().Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Revoked
	for rows.Next() {
		var r Revoked
		if err := rows.Scan(&r.Seq, &r.Hash, &r.Expires); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TrimRevokedLogins forgets the revocations past when their logins would
// have expired.
func (s *Store) TrimRevokedLogins(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM revoked_logins WHERE expires_at <= ?`, s.now().Unix())
	return err
}

// AccessChange is the number of the last change to what a profile may see
// (migration 107), 0 before any.
func (s *Store) AccessChange(ctx context.Context) (int64, error) {
	var seq int64
	err := s.read.QueryRowContext(ctx, `SELECT coalesce(max(seq), 0) FROM access_changes`).Scan(&seq)
	return seq, err
}
