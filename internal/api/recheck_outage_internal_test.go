package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/overfin/server/internal/oidc"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

// A login re-checked with the provider: a grant it refuses outright (the
// person logged out there, or was disabled) ends the login here, deleted;
// the provider out of reach, a login checked within the hour is still
// accepted, and stays.
func TestRecheckRevokedAndOutage(t *testing.T) {
	ctx := context.Background()
	box, _ := seal.New(make([]byte, 32))
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			io.WriteString(w, `{"issuer":"`+srv.URL+`","authorization_endpoint":"`+srv.URL+`/a","token_endpoint":"`+srv.URL+`/token","jwks_uri":"`+srv.URL+`/jwks"}`)
		case "/token":
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"invalid_grant"}`)
		}
	}))
	t.Cleanup(srv.Close)
	auth := func(url string) *Auth {
		return &Auth{Store: st, Logger: slog.New(slog.DiscardHandler), LoginLifetime: 30 * 24 * time.Hour,
			Provider: &oidc.Provider{Issuer: url, ClientID: "overfin", ClientSecret: "s", HTTP: srv.Client()}}
	}

	_, revoked, err := st.CreateLogin(ctx, "sub", nil, "rt", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if auth(srv.URL).current(ctx, revoked) != nil {
		t.Error("a revoked grant accepted")
	}
	if _, err := st.LoginByToken(ctx, revoked); err == nil {
		t.Error("a revoked login kept")
	}

	// Out of reach: a provider at an address nothing answers.
	gone := httptest.NewTLSServer(http.NotFoundHandler())
	goneURL := gone.URL
	gone.Close()
	_, outage, _ := st.CreateLogin(ctx, "sub", nil, "rt", time.Hour)
	if l := auth(goneURL).current(ctx, outage); l == nil || l.Subject != "sub" {
		t.Errorf("checked within the hour, the provider out of reach: %+v", l)
	}
	if _, err := st.LoginByToken(ctx, outage); err != nil {
		t.Errorf("kept through the outage: %v", err)
	}
}
