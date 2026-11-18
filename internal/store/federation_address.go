package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/overfin/server/internal/msfp"
)

// Changing an address (MSFP core section 13): this server's, which moves
// when its configured address changes, and its peers', which move once a
// hello at the new address is answered under their key.

// FederationAddress is this server's address as it federates, and, while
// it moves, the one before and when the move began. Current is "" before
// the first note (NoteFederationAddress); Former, "" when no move is under
// way.
type FederationAddress struct {
	Current string
	Former  string
	MovedAt time.Time
}

// FederationAddress reads this server's address as stored.
func (s *Store) FederationAddress(ctx context.Context) (FederationAddress, error) {
	var a FederationAddress
	for name, into := range map[string]*string{"federation-address": &a.Current, "federation-former-address": &a.Former} {
		v, err := metaValue(ctx, s.read, name)
		if err != nil {
			return a, err
		}
		*into = string(v)
	}
	at, err := metaValue(ctx, s.read, "federation-moved-at")
	if err != nil || at == nil {
		return a, err
	}
	n, err := strconv.ParseInt(string(at), 10, 64)
	if err != nil {
		return a, err
	}
	a.MovedAt = time.Unix(n, 0).UTC()
	return a, nil
}

// NoteFederationAddress notes the address this server federates under
// now. The first time, it's kept; after that, an address that differs is
// a move from the one kept, at: the one kept becomes the former (a move
// still under way loses its own former), and every peer whose requests
// weren't seen since we kept what they sign for is taken as signing for
// it. moved says whether this note was a move.
func (s *Store) NoteFederationAddress(ctx context.Context, address string, at time.Time) (moved bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	current, err := metaValue(ctx, tx, "federation-address")
	if err != nil {
		return false, err
	}
	switch string(current) {
	case address:
		return false, nil
	case "":
		if err := setMeta(ctx, tx, "federation-address", []byte(address)); err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE peers SET our_address = ? WHERE our_address = ''`, string(current)); err != nil {
		return false, err
	}
	for name, value := range map[string]string{"federation-address": address, "federation-former-address": string(current),
		"federation-moved-at": strconv.FormatInt(at.Unix(), 10)} {
		if err := setMeta(ctx, tx, name, []byte(value)); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// EndFederationMove ends a move of this server's address: its former
// address is no longer accepted.
func (s *Store) EndFederationMove(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM meta WHERE name IN ('federation-former-address', 'federation-moved-at')`)
	return err
}

// PeersOnAddress counts the peers, but those gone, whose latest request
// was signed for address: those that haven't moved from it.
func (s *Store) PeersOnAddress(ctx context.Context, address string) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM peers WHERE our_address = ? AND state != 'gone'`, address).Scan(&n)
	return n, err
}

// SetPeerOurAddress notes which of this server's addresses a peer's
// latest request was signed for.
func (s *Store) SetPeerOurAddress(ctx context.Context, id, address string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET our_address = ? WHERE id = ?`, address, id)
	return err
}

// SetPeerMove keeps the address a peer's signed hello named, to be
// confirmed (m nil clears it). A new address starts its own record of
// tries.
func (s *Store) SetPeerMove(ctx context.Context, id string, m *msfp.Move) error {
	if m == nil {
		_, err := s.db.ExecContext(ctx, `UPDATE peers SET new_address = '', new_address_tried = NULL,
			new_address_failing = NULL WHERE id = ?`, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET new_address = ?, new_address_tried = ?, new_address_failing = ?
		WHERE id = ?`, m.Address, nullTime(m.Tried), nullTime(m.Failing), id)
	return err
}

// MovePeer stores a peer's new address, confirmed, if its address is
// still from: the move recorded is cleared, and the pins learnt from the
// old address are replaced by those the confirming hello named (MSFP core
// section 13.2). moved is false when its address had changed meanwhile.
func (s *Store) MovePeer(ctx context.Context, id, from, to string, pins []string) (moved bool, err error) {
	res, err := s.db.ExecContext(ctx, `UPDATE peers SET address = ?, certificates = ?, new_address = '',
		new_address_tried = NULL, new_address_failing = NULL WHERE id = ? AND address = ?`,
		to, strings.Join(pins, " "), id, from)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// nullTime is t in Unix seconds, or NULL for the zero time.
func nullTime(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.Unix(), Valid: true}
}
