package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/overfin/server/internal/password"
)

// PasswordRules are what a new password must have; password.DefaultRules
// until the panel changes them.
func (s *Store) PasswordRules(ctx context.Context) (password.Rules, error) {
	var raw []byte
	err := s.read.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'password-rules'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return password.DefaultRules, nil
	}
	if err != nil {
		return password.DefaultRules, err
	}
	r := password.DefaultRules
	if err := json.Unmarshal(raw, &r); err != nil || !r.Valid() {
		return password.DefaultRules, err
	}
	return r, nil
}

// SetPasswordRules changes them.
func (s *Store) SetPasswordRules(ctx context.Context, r password.Rules) error {
	if !r.Valid() {
		return errors.New("store: password rules: a length from 10 to 64")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('password-rules', ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, raw)
	return err
}

// CopiedSetup is what happens to someone who chose their password from a
// setup link the admin copied, when Overfin can send emails (their
// address isn't proven by it): "reminder" (the default), they sign in and
// are asked to confirm their address; "hold", they confirm it first.
func (s *Store) CopiedSetup(ctx context.Context) (string, error) {
	var v string
	err := s.read.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'copied-setup'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && v != "hold") {
		return "reminder", nil
	}
	return v, err
}

// SetCopiedSetup changes CopiedSetup: "reminder" or "hold".
func (s *Store) SetCopiedSetup(ctx context.Context, v string) error {
	if v != "reminder" && v != "hold" {
		return fmt.Errorf("store: copied setup: %q", v)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('copied-setup', ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, []byte(v))
	return err
}
