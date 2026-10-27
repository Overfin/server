package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Notices: account events the operator is told about (an account made,
// one whose access ends soon or has ended), by email and webhook. Never
// what anyone watches.

// NoticeSettings are the notices' settings, all off until the operator
// switches them on.
type NoticeSettings struct {
	// The events: an account made, access ending within Days, ended.
	Joined   bool `json:"joined"`
	Expiring bool `json:"expiring"`
	Expired  bool `json:"expired"`
	Days     int  `json:"days"`
	// Where they go: the admins' email, a webhook (an http or https URL).
	Admins  bool   `json:"admins"`
	Webhook string `json:"webhook"`
	// Person emails whoever's access ends, Days before.
	Person bool `json:"person"`
}

// On reports whether any notice may be sent.
func (n NoticeSettings) On() bool {
	return (n.Joined || n.Expiring || n.Expired) && (n.Admins || n.Webhook != "") || n.Person
}

// NoticeSettings returns the notices' settings; with none saved, all off,
// seven days ahead.
func (s *Store) NoticeSettings(ctx context.Context) (NoticeSettings, error) {
	n := NoticeSettings{Days: 7}
	var v []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'notices'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return n, nil
	}
	if err != nil {
		return n, err
	}
	return n, json.Unmarshal(v, &n)
}

// SetNoticeSettings saves them.
func (s *Store) SetNoticeSettings(ctx context.Context, n NoticeSettings) error {
	v, err := json.Marshal(n)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('notices', ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, v)
	return err
}

const noticeSecretContext = "notice secret"

// NoticeSecret is the key webhook posts are signed with; ok is false
// without one.
func (s *Store) NoticeSecret(ctx context.Context) (secret []byte, ok bool, err error) {
	var sealed []byte
	err = s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'notice-secret'`).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	k, err := s.box.Open(sealed, noticeSecretContext)
	if err != nil {
		return nil, false, fmt.Errorf("store: the webhook's key can't be read with this secret key: %w", err)
	}
	return k, true, nil
}

// NewNoticeSecret makes a new signing key (32 random bytes), replacing
// any other, and returns it.
func (s *Store) NewNoticeSecret(ctx context.Context) ([]byte, error) {
	k := make([]byte, 32)
	rand.Read(k)
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('notice-secret', ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, s.box.Seal(k, noticeSecretContext))
	return k, err
}

// NoticesDue lists the accounts, not disabled, whose access ends after
// from and no later than until, and that haven't had a notice of kind for
// that end ("expiring", "expired" or "warned", the person's own).
func (s *Store) NoticesDue(ctx context.Context, kind string, from, until time.Time) ([]*Account, error) {
	rows, err := s.db.QueryContext(ctx, accountSelect+`
		WHERE a.disabled_at IS NULL AND a.expires_at > ? AND a.expires_at <= ?
			AND NOT EXISTS (SELECT 1 FROM notices_sent n WHERE n.account_id = a.id AND n.kind = ? AND n.expires_at = a.expires_at)
		ORDER BY a.expires_at`, from.Unix(), until.Unix(), kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// NoticeSent records a notice of kind for the account's end of access.
func (s *Store) NoticeSent(ctx context.Context, accountID, kind string, expires, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO notices_sent (account_id, kind, expires_at, sent_at) VALUES (?, ?, ?, ?)
		ON CONFLICT DO NOTHING`, accountID, kind, expires.Unix(), at.Unix())
	return err
}
