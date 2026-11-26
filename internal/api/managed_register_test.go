package api_test

import (
	"net/http"
	"testing"

	"github.com/overfin/server/internal/feature"
	"github.com/overfin/server/internal/invites"
)

// Registering: on a server that can't send email, refused (nobody could
// confirm the address); with email, tries limited per address, whatever
// its case, before anything else is looked at.
func TestManagedRegisterRefusals(t *testing.T) {
	srv, _ := newServerWith(t, options{managed: true, modules: []feature.Feature{invites.Feature{}}})
	b := newBrowser(srv)
	if c, out := signinPost(t, b, srv, "/api/v1/register", checked(t, srv, map[string]any{"email": "new@example.org"})); c != http.StatusBadRequest {
		t.Errorf("without email: %d %v", c, out)
	}

	srv, _ = newServerWith(t, options{managed: true, mailbox: &mailbox{}, modules: []feature.Feature{invites.Feature{}}})
	b = newBrowser(srv)
	limited := 0
	for i := range 12 {
		email := "New@Example.org"
		if i%2 == 0 {
			email = "new@example.org"
		}
		c, _ := signinPost(t, b, srv, "/api/v1/register", checked(t, srv, map[string]any{"email": email}))
		if c == http.StatusTooManyRequests {
			limited = i
			break
		}
	}
	if limited != 10 {
		t.Errorf("limited at try %d, want the 11th (index 10)", limited)
	}
	// Another address isn't held by that one's limit.
	if c, _ := signinPost(t, b, srv, "/api/v1/register", checked(t, srv, map[string]any{"email": "other@example.org"})); c == http.StatusTooManyRequests {
		t.Error("another address limited")
	}
	// Without the check against scripts: refused.
	if c, _ := signinPost(t, b, srv, "/api/v1/register", map[string]any{"email": "x@example.org"}); c != http.StatusBadRequest {
		t.Errorf("without the check: %d", c)
	}
}
