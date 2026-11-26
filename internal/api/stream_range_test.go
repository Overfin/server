package api

import (
	"testing"
)

// A Range: MSFP's three forms only (media section 2.1), for this
// server's own players as for its peers.
func TestValidRange(t *testing.T) {
	for h, want := range map[string]bool{
		"bytes=0-":                true,
		"bytes=100-199":           true,
		"bytes=5-5":               true,
		"bytes=-500":              true,
		"bytes=-0":                false,
		"bytes=-":                 false,
		"bytes=5-3":               false,
		"bytes=0-1,4-5":           false,
		"items=0-1":               false,
		"bytes= 0-1":              false,
		"bytes=01-2":              true, // leading zeros are accepted
		"bytes=1234567890123456-": false,
	} {
		if got := validRange(h); got != want {
			t.Errorf("%q: %v, want %v", h, got, want)
		}
	}
}
