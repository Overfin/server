package seal

import (
	"bytes"
	"testing"
)

// A sealed value opens with its own context only, any change to it is
// caught, and anything else never opens, without a panic.
func FuzzSeal(f *testing.F) {
	f.Add([]byte("token"), "prof_1", []byte{}, 0, byte(1))
	f.Add([]byte{}, "", []byte{version}, 40, byte(0x80))
	f.Fuzz(func(t *testing.T, plaintext []byte, context string, junk []byte, at int, flip byte) {
		b, err := New(bytes.Repeat([]byte{7}, 32))
		if err != nil {
			t.Fatal(err)
		}
		sealed := b.Seal(plaintext, context)
		got, err := b.Open(sealed, context)
		if err != nil || !bytes.Equal(got, plaintext) {
			t.Fatalf("round trip: %q %v", got, err)
		}
		if _, err := b.Open(sealed, context+"x"); err == nil {
			t.Fatal("opened with another context")
		}
		if flip != 0 && len(sealed) > 0 {
			i := at % len(sealed)
			if i < 0 {
				i = -i
			}
			changed := bytes.Clone(sealed)
			changed[i] ^= flip
			if _, err := b.Open(changed, context); err == nil {
				t.Fatalf("opened after byte %d changed", i)
			}
		}
		if len(sealed) > 0 {
			if _, err := b.Open(sealed[:len(sealed)-1], context); err == nil {
				t.Fatal("opened cut short")
			}
		}
		if got, err := b.Open(junk, context); err == nil {
			t.Fatalf("junk opened: %q", got)
		}
	})
}
