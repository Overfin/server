package wire

import (
	"bytes"
	"crypto/ed25519"
	"sync"

	"filippo.io/edwards25519"
)

// Ed25519 as MSFP's section 2.1 wants it, stricter than crypto/ed25519
// alone: a public key, and a signature's R, must be the one encoding of a
// point of the prime-order subgroup, never of small order (the identity
// among them), and S must be below L. crypto/ed25519 accepts a key of
// small order: under the identity, the signature (R = identity, S = 0)
// verifies every message. Honest keys and signatures are [x]B for some
// x, and always pass.
//
// filippo.io/edwards25519 is the group arithmetic crypto/ed25519 uses
// inside (the standard library doesn't export it): the reason for the
// dependency.

// lMinusOne is L − 1, where L = 2^252 + 27742317777372353535851937790883648493
// is the order of the prime-order subgroup. Scalars are taken mod L, so L
// itself isn't one: [L]P is [L − 1]P + P.
var lMinusOne = func() *edwards25519.Scalar {
	b := []byte{
		0xec, 0xd3, 0xf5, 0x5c, 0x1a, 0x63, 0x12, 0x58,
		0xd6, 0x9c, 0xf7, 0xa2, 0xde, 0xf9, 0xde, 0x14,
		0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0x10,
	}
	s, err := edwards25519.NewScalar().SetCanonicalBytes(b)
	if err != nil {
		panic(err)
	}
	return s
}()

// primeOrderPoint says whether b is the canonical encoding of a point of
// the prime-order subgroup that isn't of small order: what a public key
// and a signature's R must be.
func primeOrderPoint(b []byte) bool {
	if len(b) != 32 {
		return false
	}
	p, err := new(edwards25519.Point).SetBytes(b)
	if err != nil || !bytes.Equal(p.Bytes(), b) {
		return false // not on the curve, or y written at or above p
	}
	identity := edwards25519.NewIdentityPoint()
	// The 8 points of small order are those [8]P sends to the identity.
	if new(edwards25519.Point).MultByCofactor(p).Equal(identity) == 1 {
		return false
	}
	// In the prime-order subgroup: [L]P is the identity.
	q := new(edwards25519.Point).ScalarMult(lMinusOne, p)
	return q.Add(q, p).Equal(identity) == 1
}

// keyChecks remembers keys already checked, valid or not: a key is
// checked once (MSFP section 2.1 allows keeping the result), and a peer's
// key comes with every request. Cleared when full, so strangers sending
// keys of their own can't make it grow.
var keyChecks = struct {
	sync.Mutex
	m map[string]bool
}{m: map[string]bool{}}

const maxKeyChecks = 4096

// ValidKey says whether a public key is one MSFP accepts (section 2.1):
// 32 bytes, the canonical encoding of a point of the prime-order
// subgroup, not of small order.
func ValidKey(public []byte) bool {
	if len(public) != ed25519.PublicKeySize {
		return false
	}
	keyChecks.Lock()
	ok, seen := keyChecks.m[string(public)]
	keyChecks.Unlock()
	if seen {
		return ok
	}
	ok = primeOrderPoint(public)
	keyChecks.Lock()
	if len(keyChecks.m) >= maxKeyChecks {
		clear(keyChecks.m)
	}
	keyChecks.m[string(public)] = ok
	keyChecks.Unlock()
	return ok
}

// verify checks a signature as MSFP section 2.1 says: a valid key, R a
// point of the prime-order subgroup, S below L, then the equation (which
// crypto/ed25519 checks; with A and R in that subgroup, its check and the
// cofactored one accept the same signatures).
func verify(public ed25519.PublicKey, message, sig []byte) bool {
	if len(sig) != ed25519.SignatureSize || !ValidKey(public) || !primeOrderPoint(sig[:32]) {
		return false
	}
	if _, err := edwards25519.NewScalar().SetCanonicalBytes(sig[32:]); err != nil {
		return false // S at or above L
	}
	return ed25519.Verify(public, message, sig)
}
