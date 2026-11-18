package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/overfin/server/internal/identity"
	"github.com/overfin/server/internal/plugin"
)

// The test server's client connects from 127.0.0.1, which these tests treat
// as the trusted proxy.
var loopback = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}

type whoami struct{}

func (whoami) Name() string { return "test" }

func (whoami) Register(r *plugin.Registrar) error {
	return r.HandleFunc("GET /api/v1/whoami", func(w http.ResponseWriter, req *http.Request) {
		id, _ := identity.FromContext(req.Context())
		_, _ = w.Write([]byte(id.Subject))
	})
}

func newTestServer(t *testing.T) *httptest.Server {
	return newServerWith(t, identity.NewForwardAuth(loopback, "Remote-User", "", "|"))
}

func newServerWith(t *testing.T, ids *identity.Resolver) *httptest.Server {
	t.Helper()
	h, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)), ids.Middleware, nil, whoami{})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestHealth(t *testing.T) {
	srv := newTestServer(t)
	res, err := http.Get(srv.URL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

// Paths that look like Jellyfin or Seerr routes must not reach anything.
func TestUnknownPathsAre404(t *testing.T) {
	srv := newTestServer(t)
	for _, path := range []string{
		"/",
		"/api",
		"/api/System/Info",
		"/api/Users",
		"/api/v1/settings/main",
		"/web/index.html",
		"/api/health",
		"/api/v1/health/../../System/Info",
	} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		var body struct{ Type string }
		_ = json.NewDecoder(res.Body).Decode(&body)
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want 404", path, res.StatusCode)
		}
		if ct := res.Header.Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("GET %s: Content-Type = %q, want application/problem+json", path, ct)
		}
		if body.Type != "not-found" {
			t.Errorf("GET %s: type = %q, want not-found", path, body.Type)
		}
	}
}

// A known path with the wrong method is treated like any unknown route.
func TestWrongMethodIs404(t *testing.T) {
	srv := newTestServer(t)
	res, err := http.Post(srv.URL+"/api/v1/health", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
}

func TestProtectedRouteNeedsIdentity(t *testing.T) {
	srv := newTestServer(t)

	res, err := http.Get(srv.URL + "/api/v1/whoami")
	if err != nil {
		t.Fatal(err)
	}
	var body struct{ Type string }
	_ = json.NewDecoder(res.Body).Decode(&body)
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized || body.Type != "not-authenticated" {
		t.Errorf("without identity: status %d, type %q; want 401 not-authenticated", res.StatusCode, body.Type)
	}

	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/whoami", nil)
	req.Header.Set("Remote-User", "abc123")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || string(got) != "abc123" {
		t.Errorf("with identity: status %d, body %q; want 200 abc123", res.StatusCode, got)
	}
}

// When the peer isn't a trusted proxy, identity headers mean nothing.
func TestUntrustedPeerCannotClaimIdentity(t *testing.T) {
	other := []netip.Prefix{netip.MustParsePrefix("198.51.100.7/32")}
	srv := newServerWith(t, identity.NewForwardAuth(other, "Remote-User", "", "|"))

	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/whoami", nil)
	req.Header.Set("Remote-User", "admin")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", res.StatusCode)
	}
}

func TestHealthIsPublic(t *testing.T) {
	other := []netip.Prefix{netip.MustParsePrefix("198.51.100.7/32")}
	srv := newServerWith(t, identity.NewForwardAuth(other, "Remote-User", "", "|"))
	res, err := http.Get(srv.URL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
}

func TestCrossSiteRequestsAreRefused(t *testing.T) {
	srv := newTestServer(t)
	for site, want := range map[string]int{
		"cross-site":  http.StatusForbidden,
		"same-site":   http.StatusForbidden, // a sibling subdomain
		"same-origin": http.StatusOK,
		"none":        http.StatusOK,
		"":            http.StatusOK, // non-browser clients
	} {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/whoami", nil)
		req.Header.Set("Remote-User", "abc123")
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != want {
			t.Errorf("Sec-Fetch-Site %q: status %d, want %d", site, res.StatusCode, want)
		}
	}
}

func TestStateChangesMustBeJSON(t *testing.T) {
	srv := newTestServer(t)
	for ct, want := range map[string]int{
		"application/x-www-form-urlencoded": http.StatusUnsupportedMediaType,
		"text/plain":                        http.StatusUnsupportedMediaType,
		"multipart/form-data; boundary=x":   http.StatusUnsupportedMediaType,
		"":                                  http.StatusUnsupportedMediaType,
		"application/json; charset=utf-8":   http.StatusNotFound, // passes the check; no such route
	} {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/whoami", strings.NewReader("{}"))
		req.Header.Set("Remote-User", "abc123")
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != want {
			t.Errorf("Content-Type %q: status %d, want %d", ct, res.StatusCode, want)
		}
	}
}

// A federation call that isn't JSON, or is a browser's cross-site request,
// gets the plain 404 of a path that doesn't exist (MSFP core section 3),
// never a refusal that would say federation is here.
func TestFederationRefusalsAre404(t *testing.T) {
	srv := newTestServer(t)
	for _, edit := range []func(*http.Request){
		func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
		func(r *http.Request) { r.Header.Del("Content-Type") },
		func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
	} {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/federation/hello", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		edit(req)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound || !strings.Contains(string(body), `"not-found"`) {
			t.Errorf("%v: %d %s", req.Header, res.StatusCode, body)
		}
	}
}

// A stream's path, fetched by paired servers too, refuses POST and PUT
// with a 404, never the 415 of a request that isn't JSON (MSFP media
// section 2.2: 404 or 405).
func TestStreamMethodsAre404(t *testing.T) {
	srv := newTestServer(t)
	for _, method := range []string{"POST", "PUT", "PATCH"} {
		req, _ := http.NewRequest(method, srv.URL+"/api/v1/stream/t_abc/master.m3u8", strings.NewReader("x"))
		req.Header.Set("Content-Type", "text/plain")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound && res.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d, want 404 or 405", method, res.StatusCode)
		}
	}
}

// The control panel's and the apps' paths reaching the site (the panel on
// a listener of its own) are a plain 404, never the site's page.
func TestPanelPathsNotTheSite(t *testing.T) {
	ids := identity.NewForwardAuth(loopback, "Remote-User", "", "|")
	site := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "the site") })
	h, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)), ids.Middleware, site, whoami{})
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]int{"/apps/v1/titles": 404, "/apps": 404, "/admin/": 404, "/admin": 404, "/title/m1/x": 200, "/": 200} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != want || (want == 404 && strings.Contains(rec.Body.String(), "the site")) {
			t.Errorf("%s: %d %q", path, rec.Code, rec.Body.String())
		}
	}
}
