package msfp

import (
	"crypto/ed25519"
	"testing"
)

// Every honest key and signature passes the strict checks: keys made as
// Overfin makes them, and signatures of any message.
func TestHonestKeysPass(t *testing.T) {
	for i := range 500 {
		k := NewKey()
		if !ValidKey(k.Public()) {
			t.Fatalf("key %d refused: %x", i, k.Public())
		}
		msg := []byte{byte(i), byte(i >> 8), 'm'}
		if !verify(k.Public(), msg, k.sign(msg)) {
			t.Fatalf("signature %d refused", i)
		}
		if verify(k.Public(), append(msg, 0), k.sign(msg)) {
			t.Fatalf("signature %d passes another message", i)
		}
	}
	// The identity as a key, and its "signature" (R = identity, S = 0),
	// which crypto/ed25519 alone accepts for any message.
	identity := make(ed25519.PublicKey, 32)
	identity[0] = 1
	sig := make([]byte, 64)
	sig[0] = 1
	if !ed25519.Verify(identity, []byte("anything"), sig) {
		t.Fatal("the premise: crypto/ed25519 accepts it")
	}
	if ValidKey(identity) || verify(identity, []byte("anything"), sig) {
		t.Fatal("the identity is accepted")
	}
}
