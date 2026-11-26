package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/overfin/server/internal/api"
	"github.com/overfin/server/internal/store"
)

// The confirmation link sent again: to someone waiting for it only; the
// answer is the same for an address with no account, or a confirmed one
// (nothing to learn of who has an account); tries are limited per
// address.
func TestManagedResend(t *testing.T) {
	box := &mailbox{}
	var st *store.Store
	var m *api.Managed
	srv, _ := newServerWith(t, options{managed: true, mailbox: box, store: &st, signIn: &m})
	b := newBrowser(srv)
	ctx := context.Background()
	if _, err := st.RegisterUser(ctx, "wait@example.org", "", ""); err != nil {
		t.Fatal(err)
	}
	done, err := st.CreateUser(ctx, "done@example.org", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ConfirmUser(ctx, done.Subject); err != nil {
		t.Fatal(err)
	}
	if u, _ := st.UserByEmail(ctx, "wait@example.org"); u == nil || u.Confirmed {
		t.Fatalf("an account waiting: %+v", u)
	}
	before := len(box.all())

	for _, who := range []string{"wait@example.org", "nobody@example.org", "done@example.org"} {
		c, out := signinPost(t, b, srv, "/api/v1/signin/resend", checked(t, srv, map[string]any{"email": who}))
		if c != 200 || out["sent"] != true || len(out) != 1 {
			t.Errorf("%s: %d %v", who, c, out)
		}
	}
	m.Wait()
	mails := box.all()[before:]
	if len(mails) != 1 || mails[0].to != "wait@example.org" || mails[0].subject != "Confirmez votre adresse" || linkIn(mails[0].body, "verify") == "" {
		t.Fatalf("emails sent again: %+v", mails)
	}
	// Not without the check, nor without an address.
	if c, _ := signinPost(t, b, srv, "/api/v1/signin/resend", map[string]any{"email": "wait@example.org"}); c == http.StatusOK {
		t.Error("resent without the check")
	}
	if c, _ := signinPost(t, b, srv, "/api/v1/signin/resend", checked(t, srv, map[string]any{"email": "not an address"})); c == http.StatusOK {
		t.Error("resent to something not an address")
	}
	// Ten tries an address in 15 minutes (passwordTries), then refused,
	// whatever the case of the address.
	limited := false
	for i := range 12 {
		email := "Wait@Example.org"
		if i%2 == 0 {
			email = "wait@example.org"
		}
		if c, _ := signinPost(t, b, srv, "/api/v1/signin/resend", checked(t, srv, map[string]any{"email": email})); c == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("resend never limited")
	}
}
