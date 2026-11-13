// Package edge is the gateway's first check of a visitor's session: the
// backend signs, with its link key, the hash of the visitor's login cookie,
// and gives the signature as a second cookie (overfin-edge); the gateway
// checks it with the backend's key it pinned at pairing. A request to an
// API route that needs a session, without a login cookie and its matching
// signature, is refused at the gateway, never crossing the link. The
// backend still checks every session itself: this is a filter only
// (SECURITY.md, the gateway).
package edge

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
)

// Cookie is the signature's cookie, by its base name; LoginCookie the
// login cookie it's bound to (api.LoginCookie). Over HTTPS both carry the
// __Host- prefix (package cookie).
const (
	Cookie      = "overfin-edge"
	LoginCookie = "overfin-login"
)

func message(login string) []byte {
	sum := sha256.Sum256([]byte(login))
	return append([]byte("overfin-edge v1\n"), sum[:]...)
}

// Sign is the signature of a login cookie's value, as the cookie carries it.
func Sign(key ed25519.PrivateKey, login string) string {
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, message(login)))
}

// Verify says whether token is the backend's signature of login.
func Verify(pub ed25519.PublicKey, login, token string) bool {
	sig, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(sig) == ed25519.SignatureSize && ed25519.Verify(pub, message(login), sig)
}

// Pair reads a request's login cookie and its signature, under the same
// prefix (both __Host- or neither); ok is false when either is missing.
func Pair(r *http.Request) (login, token string, ok bool) {
	for _, prefix := range []string{"__Host-", ""} {
		l, err := r.Cookie(prefix + LoginCookie)
		if err != nil {
			continue
		}
		t, err := r.Cookie(prefix + Cookie)
		if err != nil {
			return "", "", false
		}
		return l.Value, t.Value, true
	}
	return "", "", false
}
