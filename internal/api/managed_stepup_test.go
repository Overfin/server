package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/overfin/server/internal/password"
	"github.com/overfin/server/internal/store"
)

// Confirming it's them (the step-up) with Overfin's own sign-in: not
// without being signed in, not with a wrong password; with the right
// one, it answers where to go next, and the password can be changed.
func TestManagedStepUp(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{managed: true, mailbox: &mailbox{}, store: &st})
	ctx := context.Background()
	ann, _ := st.CreateUser(ctx, "ann@example.org", false)
	hash, _ := password.Hash("a long password 1")
	st.SetPassword(ctx, ann.Subject, hash)

	stranger := newBrowser(srv)
	if c, _ := signinPost(t, stranger, srv, "/api/v1/signin", map[string]any{"password": "a long password 1", "fresh": true}); c != http.StatusUnauthorized {
		t.Errorf("a step-up signed out: %d", c)
	}

	b := newBrowser(srv)
	if c, out := signinPost(t, b, srv, "/api/v1/signin", checked(t, srv, map[string]any{"email": "ann@example.org", "password": "a long password 1"})); c != 200 {
		t.Fatalf("sign in: %d %v", c, out)
	}
	put := func(body string) int {
		t.Helper()
		req, _ := http.NewRequest("PUT", srv.URL+"/api/v1/me/password", bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		res, err := b.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return res.StatusCode
	}
	newPassword, _ := json.Marshal(map[string]string{"password": "another long password 2"})
	if c, out := signinPost(t, b, srv, "/api/v1/signin", map[string]any{"password": "wrong password 9", "fresh": true}); c != http.StatusUnauthorized || out["type"] != "signin-failed" {
		t.Errorf("a wrong step-up: %d %v", c, out)
	}
	if c, out := signinPost(t, b, srv, "/api/v1/signin", map[string]any{"password": "a long password 1", "fresh": true, "return": "/settings"}); c != 200 || out["next"] != "/settings" {
		t.Fatalf("the step-up: %d %v", c, out)
	}
	// A sign-in is a fresh login itself, and the step-up keeps it so.
	if c := put(string(newPassword)); c/100 != 2 {
		t.Errorf("changing the password after the step-up: %d", c)
	}
	u, _ := st.UserByEmail(ctx, "ann@example.org")
	if !password.Check(u.PasswordHash, "another long password 2") {
		t.Error("the password didn't change")
	}
}
