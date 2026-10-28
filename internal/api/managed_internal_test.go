package api

import (
	"fmt"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

// Every language says everything English says, and nothing else.
func TestSigninTexts(t *testing.T) {
	for lang, texts := range signinTexts {
		for key := range signinTexts["en"] {
			if texts[key] == "" {
				t.Errorf("%s: no %q", lang, key)
			}
		}
		if len(texts) != len(signinTexts["en"]) {
			t.Errorf("%s: %d texts, English has %d", lang, len(texts), len(signinTexts["en"]))
		}
	}
}

func TestPageLang(t *testing.T) {
	for header, want := range map[string]string{
		"":                        "en",
		"fr-CA,fr;q=0.9,en;q=0.8": "fr",
		"de-DE,de;q=0.9,fr;q=0.8": "fr",
		"de-DE, en-US;q=0.7":      "en",
		"FR":                      "fr",
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Accept-Language", header)
		if got := pageLang(r, nil); got != want {
			t.Errorf("%q: %s, want %s", header, got, want)
		}
	}
}

func TestCleanEmail(t *testing.T) {
	for in, ok := range map[string]bool{
		"ann@example.org":           true,
		"  ann@example.org ":        true,
		"Ann <ann@example.org>":     false,
		"ann@localhost":             false,
		"ann":                       false,
		"a@b.c, d@e.f":              false,
		"ann@example.org\r\nBcc: x": false,
	} {
		if _, got := cleanEmail(in); got != ok {
			t.Errorf("%q: %v", in, got)
		}
	}
}

// Tries are limited per email and client address, and per email from
// anywhere at a much higher cap.
func TestPasswordTries(t *testing.T) {
	m := &Managed{}
	m.init()
	now := time.Now()
	for i := range passwordTries {
		if !m.mayTry("ann@example.org", "192.0.2.1", now) {
			t.Fatalf("try %d refused", i+1)
		}
	}
	if m.mayTry("Ann@example.org", "192.0.2.1", now) {
		t.Error("one try too many from one address")
	}
	if !m.mayTry("ann@example.org", "192.0.2.2", now) || !m.mayTry("bob@example.org", "192.0.2.1", now) {
		t.Error("another address, or another email, refused")
	}
	// From many addresses, the email's own cap.
	for i := passwordTries + 1; i < passwordTriesAll; i++ {
		if !m.mayTry("ann@example.org", fmt.Sprintf("198.51.100.%d", i), now) {
			t.Fatalf("try %d refused", i+1)
		}
	}
	if m.mayTry("ann@example.org", "203.0.113.1", now) {
		t.Error("past the cap for the email")
	}
	if !m.mayTry("ann@example.org", "203.0.113.1", now.Add(15*time.Minute)) {
		t.Error("still refused a quarter hour later")
	}
}

// A flood of addresses, each in its own IPv6 /64, keeps the per-address
// limit fast and its memory bounded; one /64 counts as one address.
func TestLimiterFlood(t *testing.T) {
	m := &Managed{}
	m.init()
	now := time.Now()
	addr := func(network, host uint64) netip.Addr {
		var b [16]byte
		b[0], b[1] = 0x20, 0x01 // 2001:db8::/32, for documentation
		b[2], b[3] = 0x0d, 0xb8
		for i := range 4 {
			b[4+i] = byte(network >> (24 - 8*i))
			b[12+i] = byte(host >> (24 - 8*i))
		}
		return netip.AddrFrom16(b)
	}
	start := time.Now()
	for n := range uint64(100_000) {
		if !m.byIP.allow(clientFrom(addr(n, 1)), now) {
			t.Fatalf("network %d refused", n)
		}
	}
	took := time.Since(start)
	t.Logf("100,000 networks: %v, %v a call", took, took/100_000)
	if len(m.byIP.keys) > limiterKeys || m.byIP.order.Len() != len(m.byIP.keys) {
		t.Errorf("%d keys kept (%d in order), cap %d", len(m.byIP.keys), m.byIP.order.Len(), limiterKeys)
	}
	// Generous, for slow machines and the race detector: scanning every
	// key on each call took minutes.
	if took > 10*time.Second {
		t.Errorf("100,000 networks took %v", took)
	}
	// Many addresses of one /64 are one client.
	allowed := 0
	for h := range uint64(1000) {
		if m.byIP.allow(clientFrom(addr(1_000_000, h)), now) {
			allowed++
		}
	}
	if allowed != m.byIP.max {
		t.Errorf("one /64: %d allowed, want %d", allowed, m.byIP.max)
	}
	// A window later, everything quiet is forgotten.
	m.byIP.allow("192.0.2.1", now.Add(time.Minute))
	if len(m.byIP.keys) != 1 {
		t.Errorf("%d keys after a quiet window", len(m.byIP.keys))
	}
}
