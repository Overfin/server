package admin

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/password"
)

func localPanel(t *testing.T) http.Handler {
	t.Helper()
	hash, err := password.Hash("Panel-pass-2026!")
	if err != nil {
		t.Fatal(err)
	}
	p := &Panel{Features: &memFeatures{f: config.DefaultFeatures()}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		PanelAccount: func(context.Context) (string, string, bool, error) { return "operator", hash, true, nil }}
	return p.Handler()
}

func localSignInTry(h http.Handler, from, user, pw, origin string) *httptest.ResponseRecorder {
	form := url.Values{"username": {user}, "password": {pw}, "return": {"/admin/"}}
	req := httptest.NewRequest("POST", "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = from
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestLocalLogin(t *testing.T) {
	h := localPanel(t)

	// The form, and its stylesheet.
	for path, want := range map[string]string{"/admin/login": "text/html", "/admin/login.css": "text/css"} {
		req := httptest.NewRequest("GET", path, nil)
		req.RemoteAddr = "192.168.1.5:4000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), want) {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Header().Get("Content-Type"))
		}
	}

	if rec := localSignInTry(h, "192.168.1.5:4000", "operator", "wrong-pass-2026!", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong password: %d", rec.Code)
	}
	if rec := localSignInTry(h, "192.168.1.5:4000", "someone", "Panel-pass-2026!", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong username: %d", rec.Code)
	}
	rec := localSignInTry(h, "192.168.1.5:4000", "operator", "Panel-pass-2026!", "http://example.com")
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Set-Cookie"), cookieName+"=") {
		t.Fatalf("right login: %d %q", rec.Code, rec.Header().Get("Set-Cookie"))
	}
	// The session it opened answers.
	req := httptest.NewRequest("GET", "/admin/api/session", nil)
	req.RemoteAddr = "192.168.1.5:4000"
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	srec := httptest.NewRecorder()
	h.ServeHTTP(srec, req)
	if srec.Code != 200 || !strings.Contains(srec.Body.String(), "operator") {
		t.Errorf("session: %d %s", srec.Code, srec.Body.String())
	}

	// The panel's own form, as a browser sends it (no-referrer: "null").
	if rec := localSignInTry(h, "192.168.1.5:4000", "operator", "Panel-pass-2026!", "null"); rec.Code != http.StatusSeeOther {
		t.Errorf("origin null: %d", rec.Code)
	}
	// Another site's form: refused.
	if rec := localSignInTry(h, "192.168.1.5:4000", "operator", "Panel-pass-2026!", "https://evil.example"); rec.Code != http.StatusNotFound {
		t.Errorf("another origin: %d", rec.Code)
	}
	// From the internet: the panel isn't there.
	if rec := localSignInTry(h, "203.0.113.9:4000", "operator", "Panel-pass-2026!", ""); rec.Code != http.StatusNotFound {
		t.Errorf("public address: %d", rec.Code)
	}
}

// Five wrong tries from an address, then that address waits, the right
// password too; another address still may try.
func TestLocalLoginTries(t *testing.T) {
	h := localPanel(t)
	for i := 0; i < localPerAddress; i++ {
		localSignInTry(h, "10.20.30.7:1", "operator", "nope-nope-2026!", "")
	}
	if rec := localSignInTry(h, "10.20.30.7:1", "operator", "Panel-pass-2026!", ""); rec.Code != http.StatusTooManyRequests {
		t.Errorf("after %d wrong tries: %d", localPerAddress, rec.Code)
	}
	if rec := localSignInTry(h, "10.20.30.8:1", "operator", "Panel-pass-2026!", ""); rec.Code != http.StatusSeeOther {
		t.Errorf("another address: %d", rec.Code)
	}
}
