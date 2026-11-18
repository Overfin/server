package store

import (
	"context"
	"errors"
)

// Servers that can't be called (MSFP core section 7.5): this one, when its
// operator says so, and peers that say so.

// FederationUncallable says whether this server's operator said it can't
// be called, and the address it federates under then (made the first
// time by newAddress, then kept, whether the setting is on or off).
func (s *Store) FederationUncallable(ctx context.Context, newAddress func() string) (on bool, address string, err error) {
	v, err := metaValue(ctx, s.read, "federation-uncallable")
	if err != nil {
		return false, "", err
	}
	a, err := metaValue(ctx, s.read, "federation-uncallable-address")
	if err != nil {
		return false, "", err
	}
	if a == nil {
		// Made once: two starts racing keep the first.
		if _, err := s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('federation-uncallable-address', ?)
			ON CONFLICT (name) DO NOTHING`, []byte(newAddress())); err != nil {
			return false, "", err
		}
		if a, err = metaValue(ctx, s.db, "federation-uncallable-address"); err != nil {
			return false, "", err
		}
	}
	return string(v) == "on", string(a), nil
}

// SetFederationUncallable saves this server's operator's setting. Turned
// on, the pairing codes not used yet are withdrawn: a server that can't
// be called makes none, and nobody can redeem one at its address.
func (s *Store) SetFederationUncallable(ctx context.Context, on bool) error {
	v := "off"
	if on {
		v = "on"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setMeta(ctx, tx, "federation-uncallable", []byte(v)); err != nil {
		return err
	}
	if on {
		if _, err := tx.ExecContext(ctx, `DELETE FROM pairing_codes`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetPeerInbound keeps whether a peer can be called (InboundYes, …No,
// …Confirming). Once it can't, what it held for us is dropped (it isn't
// read any more, and nothing of it is offered for playing), with its
// reading's place, so that a reading once it can again is a whole one;
// and an address it named to be confirmed by a hello there is dropped too:
// nothing is called there.
func (s *Store) SetPeerInbound(ctx context.Context, id, inbound string) error {
	switch inbound {
	case InboundYes, InboundConfirming:
		_, err := s.db.ExecContext(ctx, `UPDATE peers SET inbound = ? WHERE id = ?`, inbound, id)
		return err
	case InboundNo:
	default:
		return errors.New("store: not a peer's inbound")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE peers SET inbound = 'no', titles_seq = 0, new_address = '',
		new_address_tried = NULL, new_address_failing = NULL WHERE id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM peer_titles WHERE peer_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetPeerAddress stores the address a peer that can't be called names in
// its signed hello, at once (MSFP core section 13.2): nothing calls it, so
// there's nothing to confirm, and no pins of it to drop.
func (s *Store) SetPeerAddress(ctx context.Context, id, address string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET address = ? WHERE id = ?`, address, id)
	return err
}

// SetPeerCallsFailed keeps what a peer's latest hello answer said of our
// calls to it: whether the last ones couldn't connect (callsFailed).
func (s *Store) SetPeerCallsFailed(ctx context.Context, id string, failed bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET calls_failed = ? WHERE id = ?`, failed, id)
	return err
}
