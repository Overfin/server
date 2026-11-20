package gateway

import (
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/overfin/server/internal/link"
)

// backendOnSocket serves link.Local on a unix socket, as the backend does
// for the gateway beside it; the handler answers the path, the visitor's
// address and whether the request came over HTTPS.
func backendOnSocket(t *testing.T, version string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backend.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	site := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.URL.Path+" from "+r.RemoteAddr+" proto="+r.Header.Get("X-Forwarded-Proto")+
			" version="+r.Header.Get(link.LocalVersion))
	})
	srv := &http.Server{Handler: link.Local(version, site)}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return path
}

func getVia(t *testing.T, h http.Handler, path, from string) (int, string) {
	t.Helper()
	rec := do(h, "GET", path, from)
	return rec.Code, rec.Body.String()
}

// Beside its backend, the gateway passes the site and the API through the
// socket, with the visitor's address, sessions unchecked (the backend
// checks them); the private-only site refuses the internet.
func TestBesideTheBackend(t *testing.T) {
	path := backendOnSocket(t, "1.0")
	s := NewSocket(path, "1.0")
	h := Handler(s, "1.0", nil, nil, quiet)
	code, body := getVia(t, h, "/api/v1/me", "192.168.1.20:5000")
	if code != 200 || !strings.HasPrefix(body, "/api/v1/me from 192.168.1.20:5000") || strings.Contains(body, "version=1.0") {
		t.Fatalf("API: %d %q", code, body)
	}
	if code, body := getVia(t, h, "/films", "10.0.0.5:1"); code != 200 || !strings.Contains(body, "proto= ") {
		t.Fatalf("page: %d %q", code, body)
	}
	if !s.Status().Connected {
		t.Fatal("not connected after answers")
	}
	lan := PrivateOnly(h)
	for from, want := range map[string]int{"192.168.1.20:5000": 200, "100.70.1.2:1": 200, "[fd00::1]:1": 200, "127.0.0.1:1": 200,
		"203.0.113.9:443": 404, "[2001:db8::1]:1": 404} {
		if code, _ := getVia(t, lan, "/films", from); code != want {
			t.Errorf("from %s: %d, want %d", from, code, want)
		}
	}
}

// A backend of another version, or none, gives the gateway's own page:
// being updated, or out of reach.
func TestBesideTheBackendDown(t *testing.T) {
	h := Handler(NewSocket(backendOnSocket(t, "2.0"), "1.0"), "1.0", nil, nil, quiet)
	if code, body := getVia(t, h, "/films", "10.0.0.5:1"); code != 503 || !strings.Contains(body, "Being updated") {
		t.Fatalf("other version: %d %q", code, body)
	}
	h = Handler(NewSocket(filepath.Join(t.TempDir(), "none.sock"), "1.0"), "1.0", nil, nil, quiet)
	if code, body := getVia(t, h, "/films", "10.0.0.5:1"); code != 503 || !strings.Contains(body, "Out of reach") {
		t.Fatalf("no backend: %d %q", code, body)
	}
}

// What a visitor sends as the gateway's own headers never reaches the
// backend as such.
func TestBesideTheBackendHeaders(t *testing.T) {
	h := Handler(NewSocket(backendOnSocket(t, "1.0"), "1.0"), "1.0", nil, nil, quiet)
	rec := do(h, "GET", "/films", "10.0.0.5:1", link.ClientAddr, "1.2.3.4:5", link.LocalVersion, "9.9", "X-Forwarded-Proto", "https")
	if b := rec.Body.String(); rec.Code != 200 || !strings.Contains(b, "from 10.0.0.5:1") || !strings.Contains(b, "proto= ") {
		t.Fatalf("%d %q", rec.Code, b)
	}
}
