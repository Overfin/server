package api

import (
	"fmt"
	"net/http/httptest"
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
