package api

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/overfin/server/internal/oidc"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

// A re-check says what became of a login's groups: one that lists them
// replaces them and extends the login, one whose ID token has no claim means
// no groups, and one with no ID token keeps them but stops extending it.
func TestRecheckGroups(t *testing.T) {
	month := 30 * 24 * time.Hour
	for _, c := range []struct {
		name       string
		idToken    bool
		groups     []string // the claim; nil for none
		needGroups bool
		adminGroup string
		wantGroups []string
		extended   bool
	}{
		{name: "still in", idToken: true, groups: []string{"members"}, needGroups: true,
			wantGroups: []string{"members"}, extended: true},
		{name: "taken out", idToken: true, groups: []string{"others"}, needGroups: true,
			wantGroups: []string{"others"}, extended: true},
		{name: "empty claim", idToken: true, groups: []string{}, needGroups: true, extended: true},
		{name: "no claim", idToken: true, needGroups: true, extended: true},
		{name: "no ID token", needGroups: true, wantGroups: []string{"members"}},
		{name: "no ID token, admin group only", adminGroup: "members", wantGroups: []string{"members"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			srv := recheckIdP(t, c.idToken, c.groups)
			box, _ := seal.New(make([]byte, 32))
			st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"), box)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.Close() })
			_, token, err := st.CreateLogin(ctx, "sub", []string{"members"}, "rt", month)
			if err != nil {
				t.Fatal(err)
			}
			a := &Auth{Store: st, Logger: slog.New(slog.DiscardHandler), LoginLifetime: month, NeedGroups: c.needGroups, AdminGroup: c.adminGroup,
				Provider: &oidc.Provider{Issuer: srv.URL, ClientID: "overfin", ClientSecret: "s", GroupsClaim: "groups", HTTP: srv.Client()}}
			// Twice: a second check mustn't extend what the first cut short.
			var after *store.Login
			var first time.Time
			for i := range 2 {
				if a.current(ctx, token) == nil {
					t.Fatal("login refused")
				}
				if after, err = st.LoginByToken(ctx, token); err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					first = after.Expires
					time.Sleep(1100 * time.Millisecond) // expiry counts in seconds
				}
			}
			if !slices.Equal(after.Groups, c.wantGroups) {
				t.Errorf("groups %q, want %q", after.Groups, c.wantGroups)
			}
			left := time.Until(after.Expires)
			if c.extended && (left < month-time.Minute || !after.Expires.After(first)) {
				t.Errorf("not extended: %s left", left)
			}
			if !c.extended && (left > shortLogin || after.Expires.After(first)) {
				t.Errorf("extended: %s left, first %s, now %s", left, first, after.Expires)
			}
		})
	}
}

// recheckIdP answers refreshes with or without an ID token and groups.
func recheckIdP(t *testing.T, idToken bool, groups []string) *httptest.Server {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]string{"issuer": srv.URL, "authorization_endpoint": srv.URL + "/authorize",
				"token_endpoint": srv.URL + "/token", "jwks_uri": srv.URL + "/jwks"})
		case "/jwks":
			json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
		case "/token":
			out := map[string]string{"refresh_token": "rt"}
			if idToken {
				now := time.Now()
				claims := map[string]any{"iss": srv.URL, "aud": "overfin", "sub": "sub", "exp": now.Add(time.Minute).Unix(), "iat": now.Unix()}
				if groups != nil {
					claims["groups"] = groups
				}
				signed := enc(map[string]string{"alg": "RS256", "kid": "k"}) + "." + enc(claims)
				sum := sha256.Sum256([]byte(signed))
				sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
				out["id_token"] = signed + "." + base64.RawURLEncoding.EncodeToString(sig)
			}
			json.NewEncoder(w).Encode(out)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}
