package wire

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"strings"
)

// Key is a server's own key pair. Ed25519 keys are small (32 bytes),
// signing is fast, and the standard library has them.
type Key struct {
	private ed25519.PrivateKey
}

// NewKey makes a new random key pair.
func NewKey() Key {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		// crypto/rand never fails on the systems Go supports.
		panic(err)
	}
	return Key{private: private}
}

// KeyFromSeed rebuilds a key pair from its 32-byte seed, as stored.
func KeyFromSeed(seed []byte) (Key, error) {
	if len(seed) != ed25519.SeedSize {
		return Key{}, errors.New("federation: a key's seed is 32 bytes")
	}
	return Key{private: ed25519.NewKeyFromSeed(seed)}, nil
}

// Seed is what to store, sealed, to rebuild the key pair later.
func (k Key) Seed() []byte { return k.private.Seed() }

// Public is the key others know this server by.
func (k Key) Public() ed25519.PublicKey {
	return k.private.Public().(ed25519.PublicKey)
}

func (k Key) sign(message []byte) []byte { return ed25519.Sign(k.private, message) }

// fingerprintBytes is how much of the key's SHA-256 the fingerprint shows:
// 15 bytes, 120 bits, 24 characters. Faking a fingerprint means finding
// another key with the same 120 bits, far beyond anyone's reach.
const fingerprintBytes = 15

// Fingerprint is a key in a form people can compare, read aloud or over
// the phone: 24 letters and digits in groups of four, "ABCD-EFGH-…".
func Fingerprint(public ed25519.PublicKey) string {
	return CommitmentFingerprint(Commitment(public))
}

// CommitmentFingerprint is the fingerprint of the key a next key
// commitment is to (section 12.5): both are made of the key's SHA-256, so
// the next key's fingerprint is known from its commitment alone, its seed
// kept offline.
func CommitmentFingerprint(commitment []byte) string {
	if len(commitment) < fingerprintBytes {
		return ""
	}
	text := base32.StdEncoding.EncodeToString(commitment[:fingerprintBytes])
	var b strings.Builder
	for i := 0; i < len(text); i += 4 {
		if i > 0 {
			b.WriteByte('-')
		}
		b.WriteString(text[i : i+4])
	}
	return b.String()
}
