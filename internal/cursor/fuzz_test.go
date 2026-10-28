package cursor

import (
	"bytes"
	"testing"
)

// A cursor comes back as it went out, and anything else is refused,
// without a panic.
func FuzzCursor(f *testing.F) {
	f.Add([]byte("payload"), "binding", "x")
	f.Fuzz(func(t *testing.T, payload []byte, binding, junk string) {
		key := []byte("cursor key")
		c, err := Encode(key, binding, payload)
		if err == nil {
			got, err := Decode(key, binding, c)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("round trip: %q %v", got, err)
			}
			if _, err := Decode(key, binding+"x", c); err == nil {
				t.Fatal("another binding took it")
			}
		}
		if got, err := Decode(key, binding, junk); err == nil {
			// Taken only as it was given out.
			if again, _ := Encode(key, binding, got); again != junk {
				t.Fatalf("%q taken for %q", junk, again)
			}
		}
		if c, err := Encode(key, binding, payload); err == nil && len(c) > 2 {
			// The same cursor with a line break in it.
			if _, err := Decode(key, binding, c[:2]+"\n"+c[2:]); err == nil {
				t.Fatal("a cursor with a line break was taken")
			}
		}
	})
}
