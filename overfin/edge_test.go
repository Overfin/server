package overfin

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/overfin/server/internal/api"
	"github.com/overfin/server/internal/edge"
	"github.com/overfin/server/internal/identity"
)

func TestEdgeCookieNames(t *testing.T) {
	if edge.LoginCookie != api.LoginCookie {
		t.Errorf("edge binds to %q, the login cookie is %q", edge.LoginCookie, api.LoginCookie)
	}
}

func TestIssueEdge(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	edgeSigner = func(login string) string { return edge.Sign(key, login) }
	t.Cleanup(func() { edgeSigner = nil })
	h := issueEdge(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	serve := func(signedIn bool, cookies string) string {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Cookie", cookies)
		if signedIn {
			r = r.WithContext(identity.With(r.Context(), identity.Identity{Subject: "s"}))
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Header().Get("Set-Cookie")
	}
	set := serve(true, "overfin-login=L")
	tok, _, _ := strings.Cut(strings.TrimPrefix(set, "overfin-edge="), ";")
	if !strings.HasPrefix(set, "overfin-edge=") || !edge.Verify(pub, "L", tok) {
		t.Fatalf("a signed-in visitor without it: %q", set)
	}
	if set := serve(true, "overfin-login=L; overfin-edge="+tok); set != "" {
		t.Errorf("already there: %q", set)
	}
	if set := serve(true, "overfin-login=M; overfin-edge="+tok); !strings.HasPrefix(set, "overfin-edge=") {
		t.Errorf("another login: %q", set)
	}
	if set := serve(false, "overfin-login=L"); set != "" {
		t.Errorf("not signed in: %q", set)
	}
}
