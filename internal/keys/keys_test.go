package keys

import (
	"bytes"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
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

// Every key, pinned for a known secret (checked against an independent
// HKDF): a label changed by mistake would make every install's stored
// PINs, tokens, links and the key check stop matching at the next start.
func TestKeysPinned(t *testing.T) {
	k, err := Derive(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		got  []byte
		want string
	}{
		"Cursor":          {k.Cursor, "9bf3d21106f0e6101370f7b5da0a7564699ad43740366d7738d50107a1246e33"},
		"LogID":           {k.LogID, "2eccf4438c6d5ff95beb11802b5a427ea9bd76e02ebbcfbde6fbeb5598c42614"},
		"TokenEncryption": {k.TokenEncryption, "84efdd6c7a27bbcdd88265e806f94051eda90afbb3147a0c15a6504517588bd8"},
		"PIN":             {k.PIN, "7c963a33fc296a5703730869aad4e0adea4055852a5b4899e9377ab1ea95af97"},
		"EmailLink":       {k.EmailLink, "586d05b3575aa2c1fe2558a226140ef9d20e22089993ff1541b313b8fc27dd0e"},
		"Proof":           {k.Proof, "29b54f1c937891001565e6056879b84ebb44104fc8ca9df146348446bdbf0fa7"},
		"Device":          {k.Device, "e6a7b52d14eedc2b1f2336b553526ff5f89a33d81c3bab5e295be8b9ba3bc8df"},
		"Etag":            {k.Etag, "e6c1506914eb3445551e1889d36fbf83b8e1a2501bffd691b646bb217aad4b58"},
		"CopyID":          {k.CopyID, "d01530eb79cf96dfc0361a5ce46cf2a2cffaa069caf03865e41779b6cb7db5b2"},
		"Check":           {k.Check, "5f59c4e84ebae68a3face429f14cf3c9d4b2f398c7f2d5c01367f419a02d4803"},
	} {
		if hex.EncodeToString(c.got) != c.want {
			t.Errorf("%s changed: %x", name, c.got)
		}
	}
}
