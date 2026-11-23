package keys

import (
	"bytes"
	"crypto/hkdf"
	"crypto/sha256"
	"testing"
)

func TestDeriveIsStableAndSeparate(t *testing.T) {
	secret := bytes.Repeat([]byte{7}, 32)
	a, err := Derive(secret)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Derive(secret)
	if err != nil {
		t.Fatal(err)
	}
	// Same secret, same keys: restarts must not invalidate cursors or tokens.
	if !bytes.Equal(a.Cursor, b.Cursor) || !bytes.Equal(a.TokenEncryption, b.TokenEncryption) ||
		!bytes.Equal(a.Etag, b.Etag) || !bytes.Equal(a.CopyID, b.CopyID) {
		t.Error("derivation is not stable")
	}
	// Every purpose gets a different key.
	all := [][]byte{a.Cursor, a.LogID, a.TokenEncryption, a.PIN, a.EmailLink, a.Proof, a.Device, a.Etag, a.CopyID, a.Check}
	for i := range all {
		for j := i + 1; j < len(all); j++ {
			if bytes.Equal(all[i], all[j]) {
				t.Errorf("keys %d and %d are equal", i, j)
			}
		}
	}
	// A different secret gives different keys.
	c, _ := Derive(bytes.Repeat([]byte{8}, 32))
	if bytes.Equal(a.Cursor, c.Cursor) {
		t.Error("different secrets produced the same key")
	}
}

// The copies' keys are HKDF's with their own labels: the same secret
// gives these exact keys on any server and release (etags and copy IDs
// peers keep don't change with an upgrade).
func TestCopyKeysLabels(t *testing.T) {
	k, err := Derive(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	for label, got := range map[string][]byte{"overfin msfp-etag v1": k.Etag, "overfin copy-id v1": k.CopyID} {
		want, _ := hkdf.Key(sha256.New, bytes.Repeat([]byte{7}, 32), nil, label, 32)
		if !bytes.Equal(got, want) || len(got) != 32 {
			t.Errorf("%s: not its label's key", label)
		}
	}
}
