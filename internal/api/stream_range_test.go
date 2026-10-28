package api

import "testing"

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
