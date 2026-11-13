package edge

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http/httptest"
	"testing"
)

func TestSignVerify(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	tok := Sign(key, "login-value")
	if !Verify(pub, "login-value", tok) {
		t.Error("the backend's own signature refused")
	}
	if Verify(pub, "another-login", tok) {
		t.Error("a signature for another login accepted")
	}
	if Verify(other, "login-value", tok) {
		t.Error("another key's check accepted")
	}
	if Verify(pub, "login-value", "garbage") {
		t.Error("garbage accepted")
	}
}

func TestPair(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Cookie", "__Host-overfin-login=L; __Host-overfin-edge=E")
	if l, e, ok := Pair(r); !ok || l != "L" || e != "E" {
		t.Errorf("secure pair: %q %q %v", l, e, ok)
	}
	r.Header.Set("Cookie", "overfin-login=L")
	if _, _, ok := Pair(r); ok {
		t.Error("a login without its signature paired")
	}
	r.Header.Set("Cookie", "__Host-overfin-login=L; overfin-edge=E")
	if _, _, ok := Pair(r); ok {
		t.Error("mixed prefixes paired")
	}
}
