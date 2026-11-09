package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// DeviceCode is a code a screen showed to be signed in from elsewhere.
type DeviceCode struct {
	Device  string
	Created time.Time
}

// CreateDeviceCode keeps a screen's code and secret, by their hashes,
// until expires.
func (s *Store) CreateDeviceCode(ctx context.Context, code, secret, device string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO device_codes (code_hash, secret_hash, device, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?)`, hashToken(code), hashToken(secret), device, s.now().Unix(), expires.Unix())
	return err
}

// PendingDeviceCode finds a live code nobody confirmed yet.
func (s *Store) PendingDeviceCode(ctx context.Context, code string) (*DeviceCode, error) {
	var d DeviceCode
	var created int64
	err := s.read.QueryRowContext(ctx, `SELECT device, created_at FROM device_codes
		WHERE code_hash = ? AND subject IS NULL AND expires_at > ?`, hashToken(code), s.now().Unix()).Scan(&d.Device, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	d.Created = time.Unix(created, 0).UTC()
	return &d, err
}

// ConfirmDeviceCode gives a live, unconfirmed code to subject: ErrNotFound
// if there's no such code (another person confirmed it first, or it
// expired).
func (s *Store) ConfirmDeviceCode(ctx context.Context, code, subject string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE device_codes SET subject = ?
		WHERE code_hash = ? AND subject IS NULL AND expires_at > ?`, subject, hashToken(code), s.now().Unix())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CollectDeviceCode is the screen asking about its code by its secret: the
// subject who confirmed it, once, the code then gone; "" while nobody has.
// ErrNotFound when there's no live code for that secret.
func (s *Store) CollectDeviceCode(ctx context.Context, secret string) (string, error) {
	var subject sql.NullString
	err := s.db.QueryRowContext(ctx, `DELETE FROM device_codes
		WHERE secret_hash = ?1 AND expires_at > ?2 AND subject IS NOT NULL RETURNING subject`,
		hashToken(secret), s.now().Unix()).Scan(&subject)
	if err == nil {
		return subject.String, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	// Not confirmed yet, or no such code.
	var one int
	err = s.read.QueryRowContext(ctx, `SELECT 1 FROM device_codes WHERE secret_hash = ? AND expires_at > ?`,
		hashToken(secret), s.now().Unix()).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return "", err
}

// DeleteExpiredDeviceCodes removes codes past their time.
func (s *Store) DeleteExpiredDeviceCodes(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM device_codes WHERE expires_at <= ?`, s.now().Unix())
	return err
}
