package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/password"
	"github.com/overfin/server/internal/store"
)

// With forward auth: a name another profile has is refused on renaming;
// a kids profile can't take another's PIN off nor remove a profile; the
// owner's removals need a fresh login, which forward auth can't confirm
// (rule 31): refused, asking to log in again, nothing removed.
func TestProfileRemovalForwardAuth(t *testing.T) {
	srv, _ := newServer(t)
	owner := ownerID(t, srv)
	make := func(body string) string {
		t.Helper()
		code, out := post(t, srv, "/api/v1/profiles", body)
		var p struct{ ID string }
		json.Unmarshal([]byte(out), &p)
		if code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", body, code, out)
		}
		return p.ID
	}
	partner := make(`{"name":"Partner"}`)
	kid := make(`{"name":"Kid","kind":"kids","maxAge":0}`)
	if code, body := put(t, srv, "PUT", "/api/v1/profiles/"+partner+"/pin", `{"pin":"4321"}`); code != 204 {
		t.Fatalf("partner's PIN: %d %s", code, body)
	}
	if code, body := put(t, srv, "PATCH", "/api/v1/profiles/"+kid, `{"name":"Partner"}`); code != http.StatusConflict {
		t.Errorf("renaming to a taken name: %d %s", code, body)
	}
	if code, body := post(t, srv, "/api/v1/profiles/"+kid+"/activate", `{}`); code != 200 {
		t.Fatalf("activate kid: %d %s", code, body)
	}
	if code, _ := put(t, srv, "DELETE", "/api/v1/profiles/"+partner+"/pin", ""); code != 403 {
		t.Errorf("the kid took the partner's PIN off: %d", code)
	}
	if code, _ := put(t, srv, "DELETE", "/api/v1/profiles/"+partner, ""); code != 403 {
		t.Errorf("the kid removed the partner: %d", code)
	}
	post(t, srv, "/api/v1/profiles/"+owner+"/activate", `{}`)
	if code, _ := put(t, srv, "DELETE", "/api/v1/profiles/"+owner, ""); code != http.StatusBadRequest {
		t.Errorf("removing the owner profile: %d", code)
	}
	for _, path := range []string{"/api/v1/profiles/" + partner + "/pin", "/api/v1/profiles/" + kid} {
		if code, body := put(t, srv, "DELETE", path, ""); code != 403 || !strings.Contains(body, "step-up-required") {
			t.Errorf("DELETE %s without a fresh login: %d %s", path, code, body)
		}
	}
	if _, list := getAs(t, srv, subject, "/api/v1/profiles"); !strings.Contains(list, kid) {
		t.Error("the kid is gone all the same")
	}
	if code, _ := post(t, srv, "/api/v1/profiles/"+partner+"/activate", `{}`); code == 200 {
		t.Error("the partner's PIN is gone all the same")
	}
}

// With Overfin's own sign-in, a fresh login: the owner takes a PIN off
// (none to take off is fine) and removes a profile, which is gone.
func TestProfileRemovalSignedIn(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{managed: true, mailbox: &mailbox{}, store: &st})
	ctx := context.Background()
	ann, _ := st.CreateUser(ctx, "ann@example.org", false)
	hash, _ := password.Hash("a long password 1")
	st.SetPassword(ctx, ann.Subject, hash)
	b := newBrowser(srv)
	if c, out := signinPost(t, b, srv, "/api/v1/signin", checked(t, srv, map[string]any{"email": "ann@example.org", "password": "a long password 1"})); c != 200 {
		t.Fatalf("sign in: %d %v", c, out)
	}
	as := func(method, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		res, err := b.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(out)
	}
	if code, _ := as("GET", "/api/v1/me", ""); code != 200 {
		if code, body := as("POST", "/api/v1/me", "{}"); code != 201 {
			t.Fatalf("account: %d %s", code, body)
		}
	}
	// Just logged in again (the step-up, as the provider's return marks
	// it): this session is fresh.
	_, list := as("GET", "/api/v1/sessions", "")
	var sessions struct {
		Items []struct {
			ID      string
			Current bool
		}
	}
	json.Unmarshal([]byte(list), &sessions)
	account, err := st.AccountBySubject(ctx, ann.Subject)
	if err != nil || len(sessions.Items) != 1 {
		t.Fatalf("account %v, sessions %s", err, list)
	}
	if err := st.SetFreshLogin(ctx, account.ID, sessions.Items[0].ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	code, out := as("POST", "/api/v1/profiles", `{"name":"Partner"}`)
	var partner struct{ ID string }
	json.Unmarshal([]byte(out), &partner)
	if code != http.StatusCreated {
		t.Fatalf("partner: %d %s", code, out)
	}
	if code, body := as("PUT", "/api/v1/profiles/"+partner.ID+"/pin", `{"pin":"4321"}`); code != 204 {
		t.Fatalf("partner's PIN: %d %s", code, body)
	}
	if code, body := as("DELETE", "/api/v1/profiles/"+partner.ID+"/pin", ""); code != 204 {
		t.Fatalf("taking the PIN off: %d %s", code, body)
	}
	if code, _ := as("DELETE", "/api/v1/profiles/"+partner.ID+"/pin", ""); code != 204 {
		t.Errorf("no PIN to take off: %d", code)
	}
	if code, body := as("DELETE", "/api/v1/profiles/"+partner.ID, ""); code != 204 {
		t.Fatalf("removing the partner: %d %s", code, body)
	}
	if _, list := as("GET", "/api/v1/profiles", ""); strings.Contains(list, partner.ID) {
		t.Error("a removed profile is still listed")
	}
	if code, _ := as("DELETE", "/api/v1/profiles/"+partner.ID, ""); code != http.StatusNotFound {
		t.Errorf("removing it again: %d", code)
	}
}
