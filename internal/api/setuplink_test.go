package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/api"
	"github.com/overfin/server/internal/password"
	"github.com/overfin/server/internal/store"
)

// madePerson is someone an admin made, as the panel makes them: a random
// password, waiting for setup.
func madePerson(t *testing.T, st *store.Store, email string) *store.User {
	t.Helper()
	ctx := context.Background()
	u, err := st.CreateUser(ctx, email, false)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := password.Hash(password.Random())
	st.SetPassword(ctx, u.Subject, hash)
	st.SetSetupPending(ctx, u.Subject, true)
	return u
}

// tokenOf is a setup link's token.
func tokenOf(t *testing.T, link string) string {
	t.Helper()
	_, token, ok := strings.Cut(link, "#setup=")
	if !ok || len(token) != 43 {
		t.Fatalf("not a setup link: %q", link)
	}
	return token
}

// An emailed setup link: the page shows the address without using the
// link up; choosing the password signs the person in, ends other logins,
// and proves the address. The link works once, and a newer one voids it.
func TestSetupLinkEmailed(t *testing.T) {
	box := &mailbox{}
	var st *store.Store
	var m *api.Managed
	srv, _ := newServerWith(t, options{managed: true, mailbox: box, store: &st, signIn: &m})
	ctx := context.Background()
	bob := madePerson(t, st, "bob@example.org")
	elsewhere, _, _ := st.CreateLogin(ctx, bob.Subject, nil, "", time.Hour)

	if _, _, err := m.SetupLink(ctx, bob.Subject, true); err != nil {
		t.Fatal(err)
	}
	mails := box.all()
	if len(mails) != 1 || mails[0].to != "bob@example.org" || (mails[0].subject != "Choose your password" && mails[0].subject != "Choisissez votre mot de passe") {
		t.Fatalf("emails: %+v", mails)
	}
	first := linkIn(mails[0].body, "setup")
	// A new one voids the first.
	m.SetupLink(ctx, bob.Subject, true)
	token := linkIn(box.all()[1].body, "setup")

	b := newBrowser(srv)
	if res, body := openPage(t, b, srv, "/setup"); res.StatusCode != 200 || !strings.Contains(body, `id="setup"`) {
		t.Fatalf("the page: %d %s", res.StatusCode, body)
	}
	if c, _ := signinPost(t, b, srv, "/api/v1/setup/look", map[string]any{"link": first}); c != 401 {
		t.Errorf("the older link: %d", c)
	}
	for i := 0; i < 2; i++ {
		if c, out := signinPost(t, b, srv, "/api/v1/setup/look", map[string]any{"link": token}); c != 200 || out["email"] != "bob@example.org" {
			t.Errorf("look %d: %d %v", i, c, out)
		}
	}
	if c, _ := signinPost(t, b, srv, "/api/v1/setup/password", map[string]any{"link": token, "password": "short"}); c != 400 {
		t.Errorf("a short password: %d", c)
	}
	if c, out := signinPost(t, b, srv, "/api/v1/setup/password", map[string]any{"link": token, "password": "bob's own, 2026"}); c != 200 || out["next"] != "/" {
		t.Fatalf("choosing: %d %v", c, out)
	}
	if _, err := st.LoginByToken(ctx, elsewhere.ID); err == nil {
		t.Error("another login still works")
	}
	u, _ := st.UserBySubject(ctx, bob.Subject)
	if !password.Check(u.PasswordHash, "bob's own, 2026") || u.SetupPending || u.Unverified || !u.Confirmed {
		t.Errorf("Bob after: %+v", u)
	}
	if c, _ := signinPost(t, b, srv, "/api/v1/setup/password", map[string]any{"link": token, "password": "again, 2027!"}); c != 401 {
		t.Errorf("the link twice: %d", c)
	}
	// Signed in: nothing to confirm.
	if res, body := visit(t, b, srv, "/api/v1/me/address", nil); res.StatusCode != 200 || !strings.Contains(body, `"unconfirmed":false`) {
		t.Errorf("address: %d %s", res.StatusCode, body)
	}
}

// A copied link, emails on, "reminder" (the default): the person signs
// in at once, their address marked unconfirmed; the site asks them to
// confirm it, and the emailed link does. They sign in again later too.
func TestSetupLinkCopiedReminder(t *testing.T) {
	box := &mailbox{}
	var st *store.Store
	var m *api.Managed
	srv, _ := newServerWith(t, options{managed: true, mailbox: box, store: &st, signIn: &m})
	ctx := context.Background()
	bob := madePerson(t, st, "bob@example.org")
	link, expires, err := m.SetupLink(ctx, bob.Subject, false)
	if err != nil || time.Until(expires) < 6*24*time.Hour || len(box.all()) != 0 {
		t.Fatalf("copied: %q %v %v %d", link, expires, err, len(box.all()))
	}
	b := newBrowser(srv)
	if c, out := signinPost(t, b, srv, "/api/v1/setup/password", map[string]any{"link": tokenOf(t, link), "password": "bob's own, 2026"}); c != 200 || out["next"] != "/" {
		t.Fatalf("choosing: %d %v", c, out)
	}
	if u, _ := st.UserBySubject(ctx, bob.Subject); !u.Confirmed || !u.Unverified || u.SetupPending {
		t.Errorf("Bob: %+v", u)
	}
	if res, body := visit(t, b, srv, "/api/v1/me/address", nil); res.StatusCode != 200 || !strings.Contains(body, `"unconfirmed":true`) {
		t.Errorf("address: %d %s", res.StatusCode, body)
	}
	// Later, on another browser: signing in works, unconfirmed.
	other := newBrowser(srv)
	if c, out := signinPost(t, other, srv, "/api/v1/signin", checked(t, srv, map[string]any{"email": "bob@example.org", "password": "bob's own, 2026"})); c != 200 || out["next"] == nil {
		t.Errorf("signing in again: %d %v", c, out)
	}
	if c, out := signinPost(t, b, srv, "/api/v1/me/address", map[string]any{}); c != 200 || out["sent"] != true {
		t.Fatalf("asking for the email: %d %v", c, out)
	}
	m.Wait()
	mails := box.all()
	if len(mails) != 1 || mails[0].to != "bob@example.org" || !strings.Contains(mails[0].body, "/signin?confirm=1#verify=") {
		t.Fatalf("emails: %+v", mails)
	}
	// Signed in, the link's page doesn't send them on before it's read.
	if res, _ := openPage(t, b, srv, "/signin?confirm=1"); res.StatusCode != 200 {
		t.Errorf("the confirming page, signed in: %d", res.StatusCode)
	}
	if c, _ := signinPost(t, b, srv, "/api/v1/signin/verify", map[string]any{"link": linkIn(mails[0].body, "verify")}); c != 200 {
		t.Errorf("confirming: %d", c)
	}
	if u, _ := st.UserBySubject(ctx, bob.Subject); u.Unverified || !u.Confirmed {
		t.Errorf("Bob confirmed: %+v", u)
	}
	if c, out := signinPost(t, b, srv, "/api/v1/me/address", map[string]any{}); c != 200 || out["sent"] != false {
		t.Errorf("nothing left to confirm: %d %v", c, out)
	}
}

// A copied link, emails on, "hold": the password is saved, the email to
// confirm the address goes at once, and nobody is signed in until it's
// opened.
func TestSetupLinkCopiedHold(t *testing.T) {
	box := &mailbox{}
	var st *store.Store
	var m *api.Managed
	srv, _ := newServerWith(t, options{managed: true, mailbox: box, store: &st, signIn: &m})
	ctx := context.Background()
	st.SetCopiedSetup(ctx, "hold")
	bob := madePerson(t, st, "bob@example.org")
	link, _, _ := m.SetupLink(ctx, bob.Subject, false)
	b := newBrowser(srv)
	c, out := signinPost(t, b, srv, "/api/v1/setup/password", map[string]any{"link": tokenOf(t, link), "password": "bob's own, 2026"})
	if c != 200 || out["held"] != true || out["email"] != "bob@example.org" || out["next"] != nil {
		t.Fatalf("held: %d %v", c, out)
	}
	if res, _ := visit(t, b, srv, "/api/v1/me/address", nil); res.StatusCode != 401 {
		t.Errorf("signed in while held: %d", res.StatusCode)
	}
	if c, out := signinPost(t, b, srv, "/api/v1/signin", checked(t, srv, map[string]any{"email": "bob@example.org", "password": "bob's own, 2026"})); c != 200 || out["unconfirmed"] != true {
		t.Errorf("signing in, held: %d %v", c, out)
	}
	m.Wait()
	mails := box.all()
	if len(mails) != 1 || linkIn(mails[0].body, "verify") == "" {
		t.Fatalf("emails: %+v", mails)
	}
	if c, out := signinPost(t, b, srv, "/api/v1/signin/verify", map[string]any{"link": linkIn(mails[0].body, "verify")}); c != 200 || out["next"] == nil {
		t.Errorf("confirming: %d %v", c, out)
	}
	if u, _ := st.UserBySubject(ctx, bob.Subject); !u.Confirmed || u.Unverified {
		t.Errorf("Bob: %+v", u)
	}
	// Removing unconfirmed registrations never removes someone made.
	held := madePerson(t, st, "held@example.org")
	st.SetupDone(ctx, held.Subject, true, true)
	if n, _ := st.DeleteUnconfirmed(ctx, time.Now().Add(time.Hour)); n != 0 {
		t.Errorf("%d removed", n)
	}
}

// Without emails: the link is copied only, and there's nothing to
// confirm.
func TestSetupLinkNoMail(t *testing.T) {
	var st *store.Store
	var m *api.Managed
	srv, _ := newServerWith(t, options{managed: true, store: &st, signIn: &m})
	ctx := context.Background()
	bob := madePerson(t, st, "bob@example.org")
	if _, _, err := m.SetupLink(ctx, bob.Subject, true); err == nil {
		t.Error("emailed without email")
	}
	link, _, err := m.SetupLink(ctx, bob.Subject, false)
	if err != nil || !strings.HasPrefix(link, "/setup#setup=") && !strings.Contains(link, "/setup#setup=") {
		t.Fatalf("copied: %q %v", link, err)
	}
	b := newBrowser(srv)
	if c, out := signinPost(t, b, srv, "/api/v1/setup/password", map[string]any{"link": tokenOf(t, link), "password": "bob's own, 2026"}); c != 200 || out["next"] != "/" {
		t.Fatalf("choosing: %d %v", c, out)
	}
	if u, _ := st.UserBySubject(ctx, bob.Subject); !u.Confirmed || u.Unverified {
		t.Errorf("Bob: %+v", u)
	}
	if res, body := visit(t, b, srv, "/api/v1/me/address", nil); res.StatusCode != 200 || !strings.Contains(body, `"unconfirmed":false`) {
		t.Errorf("address: %d %s", res.StatusCode, body)
	}
}

// A cancelled, expired or unknown link, or a disabled account, works for
// nobody; asking about links too often is refused.
func TestSetupLinkRefused(t *testing.T) {
	var st *store.Store
	var m *api.Managed
	srv, _ := newServerWith(t, options{managed: true, store: &st, signIn: &m})
	ctx := context.Background()
	bob := madePerson(t, st, "bob@example.org")
	b := newBrowser(srv)
	link, _, _ := m.SetupLink(ctx, bob.Subject, false)
	st.CancelSetupLink(ctx, bob.Subject)
	if c, _ := signinPost(t, b, srv, "/api/v1/setup/look", map[string]any{"link": tokenOf(t, link)}); c != 401 {
		t.Errorf("cancelled: %d", c)
	}
	link, _, _ = m.SetupLink(ctx, bob.Subject, false)
	st.SetUserDisabled(ctx, bob.Subject, true)
	if c, _ := signinPost(t, b, srv, "/api/v1/setup/password", map[string]any{"link": tokenOf(t, link), "password": "bob's own, 2026"}); c != 401 {
		t.Errorf("disabled: %d", c)
	}
	st.SetUserDisabled(ctx, bob.Subject, false)
	if c, _ := signinPost(t, b, srv, "/api/v1/setup/look", map[string]any{"link": "short"}); c != 401 {
		t.Errorf("not a link: %d", c)
	}
	limited := false
	for i := 0; i < 25; i++ {
		if c, _ := signinPost(t, b, srv, "/api/v1/setup/look", map[string]any{"link": strings.Repeat("a", 43)}); c == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("never limited")
	}
}
