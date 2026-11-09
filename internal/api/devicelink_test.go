package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/overfin/server/internal/password"
	"github.com/overfin/server/internal/store"
)

// A screen signed in from a phone (rule 69): the screen gets a code, the
// person signed in on the phone confirms it, and the screen, and only it,
// is signed in once.
func TestDeviceCode(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{managed: true, store: &st, site: true})
	ctx := context.Background()
	ann, _ := st.CreateUser(ctx, "ann@example.org", false)
	hash, _ := password.Hash("a long password 1")
	st.SetPassword(ctx, ann.Subject, hash)

	phone := newBrowser(srv)
	if c, out := signinPost(t, phone, srv, "/api/v1/signin", checked(t, srv, map[string]any{"email": "ann@example.org", "password": "a long password 1"})); c != 200 {
		t.Fatalf("phone signs in: %d %v", c, out)
	}

	// The sign-in page offers it; the screen gets a code and a QR code.
	tv := newBrowser(srv)
	if _, body := openPage(t, tv, srv, "/signin"); !strings.Contains(body, `id="device-start"`) {
		t.Fatal("no code on the sign-in page")
	}
	c, got := signinPost(t, tv, srv, "/api/v1/device/start", checked(t, srv, map[string]any{"return": "/"}))
	code, _ := got["code"].(string)
	qr, _ := got["qr"].(map[string]any)
	if c != 200 || len(code) != 9 || code[4] != '-' || !strings.HasSuffix(got["address"].(string), "/link") || qr["path"] == "" {
		t.Fatalf("start: %d %v", c, got)
	}
	if c, out := signinPost(t, tv, srv, "/api/v1/device/check", map[string]any{"return": "/"}); c != 200 || out["waiting"] != true {
		t.Fatalf("check before confirming: %d %v", c, out)
	}

	// Someone signed out is sent to sign in, coming back to /link.
	stranger := newBrowser(srv)
	if res, _ := openPage(t, stranger, srv, "/link"); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/signin?return=%2Flink" {
		t.Errorf("/link signed out: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if c, _ := signinPost(t, stranger, srv, "/api/v1/device/confirm", map[string]any{"code": code}); c != 401 {
		t.Errorf("confirming signed out: %d", c)
	}
	if res, body := openPage(t, phone, srv, "/link"); res.StatusCode != 200 || !strings.Contains(body, `id="link"`) {
		t.Errorf("/link signed in: %d", res.StatusCode)
	}

	// A wrong code, then the right one, typed loosely.
	if c, out := signinPost(t, phone, srv, "/api/v1/device/look", map[string]any{"code": "BBBB-BBBB"}); c != 404 || !strings.Contains(out["detail"].(string), "code") {
		t.Errorf("wrong code: %d %v", c, out)
	}
	loose := strings.ToLower(strings.ReplaceAll(code, "-", " "))
	if c, out := signinPost(t, phone, srv, "/api/v1/device/look", map[string]any{"code": loose}); c != 200 || out["device"] == nil {
		t.Errorf("look: %d %v", c, out)
	}
	if c, _ := signinPost(t, phone, srv, "/api/v1/device/confirm", map[string]any{"code": code}); c != 204 {
		t.Fatalf("confirm: %d", c)
	}
	if c, _ := signinPost(t, phone, srv, "/api/v1/device/confirm", map[string]any{"code": code}); c != 404 {
		t.Errorf("confirming twice: %d", c)
	}

	// Without the screen's secret, nobody collects it.
	if c, _ := signinPost(t, stranger, srv, "/api/v1/device/check", map[string]any{}); c != 404 {
		t.Errorf("check without the secret: %d", c)
	}
	if c, out := signinPost(t, tv, srv, "/api/v1/device/check", map[string]any{"return": "/movies"}); c != 200 || out["next"] != "/movies" {
		t.Fatalf("check, confirmed: %d %v", c, out)
	}
	// The screen is signed in as Ann: the site lets it through.
	if res, _ := visit(t, tv, srv, "/movies", map[string]string{"Sec-Fetch-Dest": "document"}); res.StatusCode != 200 {
		t.Errorf("screen on the site: %d", res.StatusCode)
	}
	// Collected once.
	if c, _ := signinPost(t, tv, srv, "/api/v1/device/check", map[string]any{}); c != 404 {
		t.Errorf("check again: %d", c)
	}
}

// Wrong codes are limited per person: guessing stops long before a code
// could be found.
func TestDeviceCodeGuessing(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{managed: true, store: &st, site: true})
	ctx := context.Background()
	ann, _ := st.CreateUser(ctx, "ann@example.org", false)
	hash, _ := password.Hash("a long password 1")
	st.SetPassword(ctx, ann.Subject, hash)
	phone := newBrowser(srv)
	signinPost(t, phone, srv, "/api/v1/signin", checked(t, srv, map[string]any{"email": "ann@example.org", "password": "a long password 1"}))

	tv := newBrowser(srv)
	_, got := signinPost(t, tv, srv, "/api/v1/device/start", checked(t, srv, map[string]any{}))
	for i := range 10 {
		if c, _ := signinPost(t, phone, srv, "/api/v1/device/confirm", map[string]any{"code": "BBBBBBB" + string(rune('B'+i))}); c != 404 {
			t.Fatalf("wrong code %d: %d", i, c)
		}
	}
	if c, _ := signinPost(t, phone, srv, "/api/v1/device/confirm", map[string]any{"code": got["code"]}); c != 429 {
		t.Errorf("after 10 wrong codes, the right one: %d", c)
	}
}

// A code asks for the check against scripts: each one is a row.
func TestDeviceCodeStartNeedsCheck(t *testing.T) {
	srv, _ := newServerWith(t, options{managed: true, site: true})
	if c, _ := signinPost(t, newBrowser(srv), srv, "/api/v1/device/start", map[string]any{}); c != 400 {
		t.Errorf("start without the check: %d", c)
	}
}

// A confirmed code nobody collected yet is a login to come: what ends the
// person's logins ends it; and someone told to choose a new password since
// doesn't get a login on the screen (that choice takes no old password).
func TestDeviceCodeEndsWithLogins(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{managed: true, store: &st, site: true})
	ctx := context.Background()
	ann, _ := st.CreateUser(ctx, "ann@example.org", false)
	hash, _ := password.Hash("a long password 1")
	st.SetPassword(ctx, ann.Subject, hash)
	phone := newBrowser(srv)
	signinPost(t, phone, srv, "/api/v1/signin", checked(t, srv, map[string]any{"email": "ann@example.org", "password": "a long password 1"}))

	confirmedCode := func() *http.Client {
		tv := newBrowser(srv)
		_, got := signinPost(t, tv, srv, "/api/v1/device/start", checked(t, srv, map[string]any{}))
		if c, _ := signinPost(t, phone, srv, "/api/v1/device/confirm", map[string]any{"code": got["code"]}); c != 204 {
			t.Fatalf("confirm: %d", c)
		}
		return tv
	}
	tv := confirmedCode()
	if err := st.DeleteOtherLogins(ctx, ann.Subject, "none"); err != nil {
		t.Fatal(err)
	}
	if c, _ := signinPost(t, tv, srv, "/api/v1/device/check", map[string]any{}); c != 404 {
		t.Errorf("collected after her logins ended: %d", c)
	}

	signinPost(t, phone, srv, "/api/v1/signin", checked(t, srv, map[string]any{"email": "ann@example.org", "password": "a long password 1"}))
	tv = confirmedCode()
	if err := st.SetMustChange(ctx, ann.Subject, true); err != nil {
		t.Fatal(err)
	}
	if c, out := signinPost(t, tv, srv, "/api/v1/device/check", map[string]any{}); c != 404 {
		t.Errorf("collected while she must choose a password: %d %v", c, out)
	}
}
