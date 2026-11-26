package admin

import (
	"maps"
	"net/http"
	"testing"
)

// Logging out of the panel ends its session on the server: the browser
// is signed out, and a copy of its cookie taken before works no more.
func TestPanelLogout(t *testing.T) {
	_, b, _, _ := signedInPanel(t)
	if rec := b.do("GET", "/admin/api/session", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("signed in: %d %s", rec.Code, rec.Body)
	}
	copied := &browser{h: b.h, cookies: maps.Clone(b.cookies)}
	if rec := b.do("POST", "/admin/api/logout", "{}", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", rec.Code)
	}
	if rec := b.do("GET", "/admin/api/session", "", nil); rec.Code == http.StatusOK {
		t.Error("the browser is still signed in")
	}
	if rec := copied.do("GET", "/admin/api/session", "", nil); rec.Code == http.StatusOK {
		t.Error("a copy of the cookie still works")
	}
	// Logging out again, or without a session, is fine.
	if rec := copied.do("POST", "/admin/api/logout", "{}", nil); rec.Code != http.StatusNoContent {
		t.Errorf("logout again: %d", rec.Code)
	}
}
