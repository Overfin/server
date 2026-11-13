package store

import (
	"context"
	"database/sql"
	"errors"
)

// PanelAccount is the control panel's own login: its username and its
// password's hash; ok is false while none was made.
func (s *Store) PanelAccount(ctx context.Context) (username, hash string, ok bool, err error) {
	err = s.read.QueryRowContext(ctx, `SELECT username, password_hash FROM panel_account WHERE id = 1`).Scan(&username, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return username, hash, err == nil, err
}

// SetPanelAccount makes the control panel's login, or replaces it (a new
// password, a new username).
func (s *Store) SetPanelAccount(ctx context.Context, username, hash string) error {
	if username == "" || hash == "" {
		return errors.New("store: the panel's login needs a username and a password")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO panel_account (id, username, password_hash, changed_at) VALUES (1, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET username = excluded.username, password_hash = excluded.password_hash, changed_at = excluded.changed_at`,
		username, hash, s.now().Unix())
	return err
}
