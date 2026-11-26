package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/overfin/server/internal/api"
	"github.com/overfin/server/internal/password"
	"github.com/overfin/server/internal/store"
)

// An emailed link works only as what it was sent for, only in its form
// (refusals of those don't use it up), and not once its account is
// disabled (then it's spent).
func TestManagedLinkRefusals(t *testing.T) {
	box := &mailbox{}
	var st *store.Store
	var m *api.Managed
	srv, _ := newServerWith(t, options{managed: true, mailbox: box, store: &st, signIn: &m})
	ctx := context.Background()
	ann, _ := st.CreateUser(ctx, "ann@example.org", false)
	hash, _ := password.Hash("a long password 1")
	st.SetPassword(ctx, ann.Subject, hash)
	b := newBrowser(srv)
	if c, _ := signinPost(t, b, srv, "/api/v1/reset", checked(t, srv, map[string]any{"email": "ann@example.org"})); c != 200 {
		t.Fatal("asking a link")
	}
	m.Wait()
	token := linkIn(box.all()[0].body, "reset")
	if len(token) != 43 {
		t.Fatalf("the link: %q", token)
	}
	reset := func(link string) int {
		t.Helper()
		c, _ := signinPost(t, b, srv, "/api/v1/reset/password", map[string]any{"link": link, "password": "a new one, 2026"})
		return c
	}
	for _, bad := range []string{token[:42], token + "x", strings.ToUpper(token), ""} {
		if c := reset(bad); c != 401 && c != 400 {
			t.Errorf("link %q: %d", bad, c)
		}
	}
	// As a confirmation link it opens nothing: links are bound to what
	// they were sent for.
	if c, _ := signinPost(t, b, srv, "/api/v1/signin/verify", map[string]any{"link": token, "password": "a new one, 2026"}); c == 200 {
		t.Error("a reset link confirmed an address")
	}
	// Those refusals didn't use it up.
	if c := reset(token); c != 200 {
		t.Fatalf("the link after the refused tries: %d", c)
	}

	// An account disabled since its link was sent: the link opens nothing,
	// and is spent (enabled again, a new link is asked).
	bob, _ := st.CreateUser(ctx, "bob@example.org", false)
	st.SetPassword(ctx, bob.Subject, hash)
	before := len(box.all())
	signinPost(t, b, srv, "/api/v1/reset", checked(t, srv, map[string]any{"email": "bob@example.org"}))
	m.Wait()
	bobs := linkIn(box.all()[before].body, "reset")
	st.SetUserDisabled(ctx, bob.Subject, true)
	if c := reset(bobs); c != 401 {
		t.Errorf("a disabled account's link: %d", c)
	}
	st.SetUserDisabled(ctx, bob.Subject, false)
	if c := reset(bobs); c != 401 {
		t.Errorf("that link once the account is enabled again: %d", c)
	}
}
