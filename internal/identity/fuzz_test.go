package identity

import (
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// Whatever headers arrive, an identity is believed only from the trusted
// proxy, with a plain subject and at most maxGroups clean group names;
// the client address is the peer's unless the proxy gave one.
func FuzzResolve(f *testing.F) {
	f.Add(true, "abc123", "media|family", "192.0.2.1", "|")
	f.Add(true, "abc123", "authentik Admins|Utilisateurs médias", "192.0.2.1, 192.0.2.2", ",")
	f.Add(false, "admin", "admins", "::ffff:192.0.2.1", "|")
	f.Add(true, "abc\x00def", "bad\x07name|", "fe80::1%eth0", "|")
	f.Add(true, "abc", strings.Repeat("g|", maxGroups+1), "", "|")
	f.Fuzz(func(t *testing.T, fromProxy bool, user, groups, fwd, sep string) {
		res := NewForwardAuth(proxy, "Remote-User", "Remote-Groups", sep)
		req := httptest.NewRequest("GET", "/api/v1/me", nil)
		req.RemoteAddr = untrustedPeer
		if fromProxy {
			req.RemoteAddr = trustedPeer
		}
		req.Header.Set("Remote-User", user)
		req.Header.Set("Remote-Groups", groups)
		req.Header.Set("X-Forwarded-For", fwd)
		id, ok, client := res.Resolve(req)
		if !fromProxy && (ok || client.String() != "203.0.113.50") {
			t.Fatalf("untrusted peer: %+v %v %v", id, ok, client)
		}
		if !ok {
			if id.Subject != "" || id.Groups != nil {
				t.Fatalf("refused but %+v", id)
			}
			return
		}
		if id.Subject != user || !ValidSubject(id.Subject) {
			t.Fatalf("subject %q from %q", id.Subject, user)
		}
		if len(id.Groups) > maxGroups {
			t.Fatalf("%d groups", len(id.Groups))
		}
		for _, g := range id.Groups {
			if !validGroup(g) || g != strings.TrimSpace(g) || !utf8.ValidString(g) {
				t.Fatalf("group %q", g)
			}
			if sep != "" && strings.Contains(g, sep) {
				t.Fatalf("group %q holds the separator %q", g, sep)
			}
		}
		if client.Is4In6() || !client.IsValid() {
			t.Fatalf("client %v", client)
		}
	})
}
