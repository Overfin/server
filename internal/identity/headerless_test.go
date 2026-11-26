package identity

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// With Overfin's own login, no identity comes from the request: identity
// headers are ignored whoever sends them, a trusted proxy too. The proxy
// is still believed for the client's address; anyone else isn't.
func TestHeaderlessIgnoresIdentityHeaders(t *testing.T) {
	res := NewHeaderless(proxy)
	for _, peer := range []string{trustedPeer, untrustedPeer} {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = peer
		req.Header.Set("Remote-User", "admin")
		req.Header.Set("Remote-Groups", "overfin-admins")
		req.Header.Set("X-Forwarded-For", "192.0.2.9")
		id, ok, client := res.Resolve(req)
		if ok || id.Subject != "" {
			t.Errorf("%s: an identity from headers: %+v", peer, id)
		}
		want := netip.MustParseAddrPort(peer).Addr()
		if peer == trustedPeer {
			want = netip.MustParseAddr("192.0.2.9")
		}
		if client != want {
			t.Errorf("%s: client %v, want %v", peer, client, want)
		}
	}
}

// Middleware puts what a request resolves to in its context: the
// identity when there is one, the client's address always.
func TestMiddleware(t *testing.T) {
	var got []string
	h := NewForwardAuth(proxy, "Remote-User", "", "|").Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := FromContext(r.Context())
		got = append(got, fmt.Sprintf("%v %s %s", ok, id.Subject, ClientIP(r.Context())))
	}))
	for _, c := range []struct{ peer, user string }{{trustedPeer, "abc"}, {untrustedPeer, "abc"}, {trustedPeer, ""}} {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = c.peer
		if c.user != "" {
			req.Header.Set("Remote-User", c.user)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	want := []string{"true abc 198.51.100.7", "false  203.0.113.50", "false  198.51.100.7"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q, want %q", got, want)
	}
	// With puts an identity there from elsewhere (a login cookie).
	if id, ok := FromContext(With(context.Background(), Identity{Subject: "s"})); !ok || id.Subject != "s" {
		t.Errorf("with: %+v %v", id, ok)
	}
	if _, ok := FromContext(context.Background()); ok {
		t.Error("an identity from nowhere")
	}
	if ip := ClientIP(context.Background()); ip.IsValid() {
		t.Errorf("a client address from nowhere: %v", ip)
	}
}

// Group names kept: trimmed, the unusable ones dropped, at most 100.
func TestCleanGroups(t *testing.T) {
	if got := CleanGroups([]string{" media ", "", "bad\x07name", strings.Repeat("a", 129), "family"}); strings.Join(got, ",") != "media,family" {
		t.Errorf("cleaned: %q", got)
	}
	many := make([]string, 150)
	for i := range many {
		many[i] = fmt.Sprint("g", i)
	}
	if got := CleanGroups(many); len(got) != 100 || got[99] != "g99" {
		t.Errorf("many: %d", len(got))
	}
	if got := CleanGroups(nil); got == nil || len(got) != 0 {
		t.Errorf("none: %#v", got)
	}
}
