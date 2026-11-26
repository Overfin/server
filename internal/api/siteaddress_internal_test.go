package api

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"
)

// The site's address a screen is shown to sign it in: the configured
// one, without a trailing slash; without one, the address the screen's
// own request came to, https when it came that way (or a proxy says so).
func TestSiteAddress(t *testing.T) {
	r := httptest.NewRequest("POST", "http://tv.lan:8080/api/v1/device/start", nil)
	if got := (&Managed{SiteURL: "https://media.example.org/"}).siteAddress(r); got != "https://media.example.org" {
		t.Errorf("configured: %q", got)
	}
	m := &Managed{}
	if got := m.siteAddress(r); got != "http://tv.lan:8080" {
		t.Errorf("plain: %q", got)
	}
	r.TLS = &tls.ConnectionState{}
	if got := m.siteAddress(r); got != "https://tv.lan:8080" {
		t.Errorf("over TLS: %q", got)
	}
	r = httptest.NewRequest("POST", "http://media.example.org/api/v1/device/start", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := m.siteAddress(r); got != "https://media.example.org" {
		t.Errorf("behind a proxy: %q", got)
	}
}
