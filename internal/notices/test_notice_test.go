package notices

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/overfin/server/internal/store"
)

// The control panel's test: sent where the settings say, each failure
// said plainly; nowhere chosen, said so; a webhook's address (it may hold
// a token) never in what's said; a redirect not followed; another status
// said by its number.
func TestTestNotice(t *testing.T) {
	ctx := context.Background()
	n, db, m, h, hookURL := setup(t)
	db.CreateUser(ctx, "admin@example.org", true)
	db.CreateUser(ctx, "person@example.org", false)

	if _, err := n.Test(ctx, store.NoticeSettings{}); err == nil || !strings.Contains(err.Error(), "choose where") {
		t.Errorf("nowhere: %v", err)
	}
	got, err := n.Test(ctx, store.NoticeSettings{Admins: true, Webhook: hookURL})
	if err != nil || got != "Sent to the admins' email and the webhook." {
		t.Fatalf("both: %q %v", got, err)
	}
	if sent := m.all(); len(sent) != 1 || sent[0].to != "admin@example.org" {
		t.Errorf("emailed: %+v", sent)
	}
	if posts := h.got(); len(posts) != 1 || posts[0].Event != "test" || posts[0].Account != nil {
		t.Errorf("webhook: %+v", posts)
	}

	// Without Overfin's own sign-in, or without email: the admins' part
	// fails, said why; the webhook still sent.
	n.Managed = false
	if _, err := n.Test(ctx, store.NoticeSettings{Admins: true, Webhook: hookURL}); err == nil ||
		err.Error() != "Sent to the webhook. Failed: the admins' email: only with Overfin's own sign-in." {
		t.Errorf("not managed: %v", err)
	}
	n.Managed, n.Mail = true, func() Sender { return nil }
	if _, err := n.Test(ctx, store.NoticeSettings{Admins: true}); err == nil || !strings.Contains(err.Error(), "email isn't set up") {
		t.Errorf("no email: %v", err)
	}

	// A webhook that redirects, one that fails, one that isn't there.
	redirect := httptest.NewServer(http.RedirectHandler(hookURL, http.StatusFound))
	defer redirect.Close()
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer failing.Close()
	gone := httptest.NewServer(http.NotFoundHandler())
	goneURL := gone.URL + "/api/webhooks/1234/secret-token"
	gone.Close()
	before := len(h.got())
	for address, want := range map[string]string{
		redirect.URL + "/hook": "it answered 302",
		failing.URL:            "it answered 500",
		goneURL:                "the webhook: ",
	} {
		_, err := n.Test(ctx, store.NoticeSettings{Webhook: address})
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "secret-token") {
			t.Errorf("%s: %v", address, err)
		}
	}
	if len(h.got()) != before {
		t.Error("a redirect was followed")
	}
}
