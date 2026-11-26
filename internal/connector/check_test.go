package connector

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The connector of each kind the configuration names, none for another;
// a refused credential told apart from other failures, wrapped or not.
func TestNewAndRefused(t *testing.T) {
	if _, ok := New("authentik", "https://a.example.org", "t", nil).(*Authentik); !ok {
		t.Error("authentik")
	}
	if _, ok := New("pocketid", "https://p.example.org", "k", nil).(*PocketID); !ok {
		t.Error("pocketid")
	}
	if New("keycloak", "https://k.example.org", "k", nil) != nil {
		t.Error("a kind that isn't one")
	}
	if !Refused(fmt.Errorf("x: %w", ErrUnauthorized)) || !Refused(ErrPocketIDKey) || Refused(ErrNotFound) || Refused(nil) {
		t.Error("refused")
	}
}

// Authentik's check reads one user with the token: refused, said as such;
// Pocket ID's sign-up setting read from its configuration, "" when it
// doesn't say.
func TestChecks(t *testing.T) {
	signups := `[{"key":"appName","value":"Pocket ID"},{"key":"allowUserSignups","value":"withToken"}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/core/users/" && r.URL.Query().Get("page_size") == "1":
			if r.Header.Get("Authorization") != "Bearer good" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Write([]byte(`{"results":[]}`))
		case r.URL.Path == "/api/application-configuration":
			w.Write([]byte(signups))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	if err := (&Authentik{BaseURL: srv.URL + "/", Token: "good", HTTP: srv.Client()}).Check(ctx); err != nil {
		t.Errorf("a good token: %v", err)
	}
	if err := (&Authentik{BaseURL: srv.URL, Token: "bad", HTTP: srv.Client()}).Check(ctx); !Refused(err) {
		t.Errorf("a bad token: %v", err)
	}
	p := &PocketID{BaseURL: srv.URL, Key: "k", HTTP: srv.Client()}
	if s, err := p.Signups(ctx); err != nil || s != "withToken" {
		t.Errorf("sign-ups: %q %v", s, err)
	}
	signups = `[{"key":"appName","value":"Pocket ID"}]`
	if s, err := p.Signups(ctx); err != nil || s != "" {
		t.Errorf("not said: %q %v", s, err)
	}
}
