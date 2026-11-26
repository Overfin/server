package api

import (
	"testing"
)

func TestValidRange(t *testing.T) {
	for h, want := range map[string]bool{
		"bytes=0-":                true,
		"bytes=100-199":           true,
		"bytes=5-5":               true,
		"bytes=-500":              true,
		"bytes=-":                 false,
		"bytes=5-3":               false,
		"bytes=0-1,4-5":           false,
		"items=0-1":               false,
		"bytes=1234567890123456-": false,
	} {
		if got := validRange(h); got != want {
			t.Errorf("%q: %v", h, got)
		}
	}
}

// A paired server's Range: MSFP's three forms only (media section 2.1),
// stricter than what Jellyfin takes.
func TestValidPeerRange(t *testing.T) {
	for h, want := range map[string]bool{
		"bytes=0-":                true,
		"bytes=100-199":           true,
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
		if got := validPeerRange(h); got != want {
			t.Errorf("%q: %v, want %v", h, got, want)
		}
		// Never more lenient than the browsers' rule.
		if validPeerRange(h) && !validRange(h) {
			t.Errorf("%q: a paired server's range Jellyfin's rule refuses", h)
		}
	}
}
