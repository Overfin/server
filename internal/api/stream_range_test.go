package api

import (
	"net/http"
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

// A paired server's Range is one of MSFP's three forms (media section
// 2.1), "bytes=-0" not one of them.
func TestParseRange(t *testing.T) {
	for h, want := range map[string]bool{
		"bytes=0-": true, "bytes=007-9": true, "bytes=-1": true, "bytes=5-5": true,
		"bytes=-0": false, "bytes=-": false, "bytes=5-3": false, "bytes=0-1,4-5": false, "bytes= 0-1": false,
		"bytes=1234567890123456-": false, "bytes=0-٣": false,
	} {
		if _, got := parseRange(h); got != want {
			t.Errorf("%q: %v", h, got)
		}
	}
}

// A 206 answers the range asked, or it is refused (MSFP media, section
// 2.1).
func TestCheckPartial(t *testing.T) {
	for _, c := range []struct {
		rangeHeader, contentRange, contentLength string
		n                                        int64
	}{
		{"bytes=0-9", "bytes 0-9/100", "10", 10},
		{"bytes=0-99", "bytes 0-9/100", "", 10}, // less than asked
		{"bytes=0-", "bytes 0-99/*", "", 100},
		{"bytes=-10", "bytes 90-99/100", "", 10},
		{"bytes=-500", "bytes 0-99/100", "", 100},
		{"bytes=0-9", "bytes 0-9/100", "11", -1},
		{"bytes=0-9", "bytes 1-9/100", "", -1},
		{"bytes=0-9", "bytes 0-10/100", "", -1},
		{"bytes=0-", "bytes 0-100/100", "", -1},
		{"bytes=0-", "bytes 9-5/100", "", -1},
		{"bytes=-10", "bytes 90-99/*", "", -1},
		{"bytes=0-", "bytes 0-9", "", -1},
		{"bytes=0-", "", "", -1},
		{"", "bytes 0-9/100", "", -1},
	} {
		h := http.Header{}
		if c.contentRange != "" {
			h.Set("Content-Range", c.contentRange)
		}
		if c.contentLength != "" {
			h.Set("Content-Length", c.contentLength)
		}
		br, _ := parseRange(c.rangeHeader)
		n, ok := checkPartial(c.rangeHeader, br, h)
		if ok != (c.n >= 0) || (ok && n != c.n) {
			t.Errorf("%+v: %d %v", c, n, ok)
		}
	}
}
