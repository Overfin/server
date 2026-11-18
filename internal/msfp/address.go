package msfp

import (
	"net/url"
	"time"
)

// Changing a server's address (core section 13): a new name, a new port,
// without pairing again.
//
// The server that moves from A1 to A2 accepts requests signed for either
// host for AddressOverlap, or until every peer signs for A2's (Verifier's
// Former), and names A2 in every hello it sends and answers. A peer that
// reads A2 in a signed hello keeps calling A1 until a hello at A2, signed
// for A2's host, is answered under the mover's key: only then does it
// store A2 (Announced, Move).

// AddressOverlap is how long a server that moved still accepts requests
// signed for its former host (core section 13.1, step 1): 30 days, as for
// keys (section 18, point 5).
const AddressOverlap = 30 * 24 * time.Hour

// MoveRetry is how often, at most, a peer calls an announced address again
// after a confirming hello there failed (section 13.2, step 3).
const MoveRetry = time.Hour

// MoveWarn is how long the confirmation of a new address may keep failing
// before the operator is warned (section 13.2, step 3).
const MoveWarn = 24 * time.Hour

// Host is an address's host, with its port if it has one: what requests
// for that server are signed for (section 6.2). "" for an address that
// doesn't read as one.
func Host(address string) string {
	u, err := url.Parse(address)
	if err != nil {
		return ""
	}
	return u.Host
}

// Announced reads the address a signed hello, request or answer, names
// for its sender, against the one stored for it (core section 13.2,
// step 1): "" when it names none, or the one stored, and there is nothing
// to do; otherwise the address to confirm. One that isn't of section
// 5.2's form is an error: the stored one stays.
func Announced(stored, address string) (string, error) {
	if address == "" || address == stored {
		return "", nil
	}
	if err := CheckAddress(address); err != nil {
		return "", err
	}
	return address, nil
}

// Move is a peer's announced address, not confirmed yet (core section
// 13.2): until a hello there is answered under its key, the stored
// address stays.
type Move struct {
	// Address is the address announced.
	Address string
	// Tried is when a confirming hello was last sent there; zero before
	// the first.
	Tried time.Time
	// Failing is when the confirmation first failed; zero while none has.
	Failing time.Time
}

// Due says whether the confirming hello may be sent now: the first at
// once, then at most once every MoveRetry after a failure.
func (m Move) Due(now time.Time) bool {
	return m.Tried.IsZero() || m.Failing.IsZero() || now.Sub(m.Tried) >= MoveRetry
}

// Warn says whether the operator should be told: the confirmation has
// been failing for MoveWarn.
func (m Move) Warn(now time.Time) bool {
	return !m.Failing.IsZero() && now.Sub(m.Failing) >= MoveWarn
}
