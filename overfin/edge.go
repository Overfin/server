package overfin

import (
	"net/http"

	"github.com/overfin/server/internal/cookie"
	"github.com/overfin/server/internal/edge"
	"github.com/overfin/server/internal/identity"
)

// edgeSigner, on the backend, signs a login cookie for the gateway's first
// check (package edge): the link's own key, which the gateway pinned.
var edgeSigner func(login string) string

// edgeLifetime is the signature cookie's: it's worth nothing without its
// login cookie, whose own lifetime governs.
const edgeLifetime = 400 * 24 * 3600

// issueEdge gives a signed-in visitor the signature of their login cookie
// when they don't have it (a new sign-in, a session from before the
// gateway, another login): their next requests to the API's session
// routes then pass the gateway's check. Pages aren't checked there, so a
// page load always brings it.
func issueEdge(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := identity.FromContext(r.Context()); ok && edgeSigner != nil {
			if c, err := cookie.Get(r, edge.LoginCookie); err == nil && c.Value != "" {
				want := edgeSigner(c.Value)
				if have, err := cookie.Get(r, edge.Cookie); err != nil || have.Value != want {
					cookie.Set(w, r, &http.Cookie{Name: edge.Cookie, Value: want, MaxAge: edgeLifetime, SameSite: http.SameSiteLaxMode})
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
