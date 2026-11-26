package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/overfin/server/internal/identity"
	"github.com/overfin/server/internal/provision"
)

// Without Overfin's own sign-in, the site's pages are for whoever the
// reverse proxy vouched for, if allowed: nobody vouched for is told to
// sign in; someone anonymous, or outside the required group, is refused;
// a member gets the page.
func TestSiteGateForwardAuth(t *testing.T) {
	prov := &provision.Provisioner{RequiredGroup: "friends", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	gate := SiteGate(nil, prov)
	for _, c := range []struct {
		name   string
		id     *identity.Identity
		admit  bool
		status int
		typ    string
	}{
		{"nobody vouched for", nil, false, 401, "not-authenticated"},
		{"anonymous", &identity.Identity{Anonymous: true}, false, 403, "account-not-allowed"},
		{"outside the group", &identity.Identity{Subject: "s1", Groups: []string{"family"}}, false, 403, "account-not-allowed"},
		{"no groups at all", &identity.Identity{Subject: "s2"}, false, 403, "account-not-allowed"},
		{"a member", &identity.Identity{Subject: "s3", Groups: []string{"family", "friends"}}, true, 200, ""},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if c.id != nil {
			r = r.WithContext(identity.With(r.Context(), *c.id))
		}
		w := httptest.NewRecorder()
		if got := gate(w, r); got != c.admit {
			t.Errorf("%s: admitted %v", c.name, got)
		}
		if w.Code != c.status || !strings.Contains(w.Body.String(), c.typ) {
			t.Errorf("%s: %d %s", c.name, w.Code, w.Body)
		}
	}
	// With no group required, anyone vouched for (not anonymous) gets in.
	open := SiteGate(nil, &provision.Provisioner{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(identity.With(httptest.NewRequest("GET", "/", nil).Context(), identity.Identity{Subject: "s"}))
	if !open(httptest.NewRecorder(), r) {
		t.Error("no group required: refused")
	}
}
