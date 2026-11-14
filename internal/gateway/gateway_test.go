package gateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/overfin/server/internal/edge"
	"github.com/overfin/server/internal/link"
)

// fakeBackend answers with the path and the visitor's address it got.
type fakeBackend struct {
	connected bool
	mismatch  string
	seen      []string
	pub       ed25519.PublicKey
}

func (f *fakeBackend) BackendKey() ed25519.PublicKey { return f.pub }

func (f *fakeBackend) RoundTrip(r *http.Request) (*http.Response, error) {
	if !f.connected {
		return nil, link.ErrNoBackend
	}
	f.seen = append(f.seen, r.Method+" "+r.URL.Path)
	body := r.URL.Path + " from " + r.Header.Get(link.ClientAddr) + " xff=" + r.Header.Get("X-Forwarded-For")
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func (f *fakeBackend) Status() link.Status {
	return link.Status{Connected: f.connected, Mismatch: f.mismatch}
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func do(h http.Handler, method, path, from string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = from
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAllowlist(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	b := &fakeBackend{connected: true, pub: pub}
	h := Handler(b, "1.0", nil, nil, quiet)
	signedIn := "overfin-login=L; overfin-edge=" + edge.Sign(key, "L")
	for _, c := range []struct {
		method, path string
		passes       bool
	}{
		{"GET", "/api/v1/health", true},
		{"POST", "/api/v1/signin", true},
		{"GET", "/api/v1/titles/abc123", true},
		{"GET", "/api/v1/stream/tk/segment-3.ts", true},
		{"POST", "/api/v1/federation/titles", true},
		{"GET", "/", true},
		{"GET", "/title/m550988/free-guy", true},
		{"GET", "/signin", true},
		{"HEAD", "/", true},
		// Never through the gateway.
		{"GET", "/admin/", false},
		{"GET", "/admin/api/session", false},
		{"POST", "/admin/login", false},
		{"GET", "/apps/v1/titles", false},
		{"GET", "/link/v1/status", false},
		{"GET", "/api/v1/not-a-route", false},
		{"DELETE", "/api/v1/health", false},
		{"POST", "/title/m550988/free-guy", false},
		{"PUT", "/", false},
	} {
		b.seen = nil
		rec := do(h, c.method, c.path, "203.0.113.5:4000", "Cookie", signedIn)
		if passed := len(b.seen) == 1; passed != c.passes {
			t.Errorf("%s %s: passed %v (%d)", c.method, c.path, passed, rec.Code)
		}
		if !c.passes && rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s refused with %d", c.method, c.path, rec.Code)
		}
	}
}

func TestClientAddress(t *testing.T) {
	b := &fakeBackend{connected: true}
	h := Handler(b, "1.0", nil, nil, quiet)

	// A visitor naming someone else: ignored.
	rec := do(h, "GET", "/", "203.0.113.5:4000", "X-Forwarded-For", "198.51.100.9", "X-Real-Ip", "198.51.100.9")
	if got := rec.Body.String(); !strings.Contains(got, "from 203.0.113.5:4000") || strings.Contains(got, "198.51.100.9") {
		t.Errorf("spoofed: %s", got)
	}

	// Behind a trusted HTTP proxy: the last untrusted hop.
	HTTPProxies = []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}
	t.Cleanup(func() { HTTPProxies = nil })
	rec = do(h, "GET", "/", "10.20.0.2:5000", "X-Forwarded-For", "198.51.100.1, 192.0.2.44, 10.20.0.3")
	if got := rec.Body.String(); !strings.Contains(got, "from 192.0.2.44:0") {
		t.Errorf("behind a proxy: %s", got)
	}
}

func TestDownPages(t *testing.T) {
	b := &fakeBackend{}
	h := Handler(b, "1.0", nil, nil, quiet)
	rec := do(h, "GET", "/", "203.0.113.5:4000", "Accept-Language", "fr-CA,fr;q=0.9")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "Hors d’atteinte") || rec.Header().Get("Retry-After") == "" {
		t.Errorf("unreachable, French: %d %s", rec.Code, rec.Body.String())
	}
	b.mismatch = "0.9"
	rec = do(h, "GET", "/api/v1/health", "203.0.113.5:4000")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "server-updating") {
		t.Errorf("updating, API: %d %s", rec.Code, rec.Body.String())
	}
	for _, word := range []string{"Jellyfin", "backend", "media server"} {
		if strings.Contains(do(h, "GET", "/", "203.0.113.5:1").Body.String(), word) {
			t.Errorf("the page names %q", word)
		}
	}
}

func TestProxyHeaders(t *testing.T) {
	// Version 1, then the connection's own bytes untouched.
	r := bytes.NewReader([]byte("PROXY TCP4 198.51.100.7 192.0.2.10 51000 443\r\nHELLO"))
	a, err := readProxyHeader(r)
	if err != nil || a.String() != "198.51.100.7:51000" {
		t.Fatalf("v1: %v %v", a, err)
	}
	if rest, _ := io.ReadAll(r); string(rest) != "HELLO" {
		t.Errorf("v1 read too much: %q", rest)
	}
	// Version 2, IPv6.
	var h bytes.Buffer
	h.Write(v2Signature)
	h.Write([]byte{0x21, 0x21})
	binary.Write(&h, binary.BigEndian, uint16(36))
	src := netip.MustParseAddr("2001:db8::7").As16()
	h.Write(src[:])
	h.Write(make([]byte, 16))
	binary.Write(&h, binary.BigEndian, uint16(4433))
	binary.Write(&h, binary.BigEndian, uint16(443))
	h.WriteString("TLS")
	r = bytes.NewReader(h.Bytes())
	a, err = readProxyHeader(r)
	if err != nil || a.String() != "[2001:db8::7]:4433" {
		t.Fatalf("v2: %v %v", a, err)
	}
	if rest, _ := io.ReadAll(r); string(rest) != "TLS" {
		t.Errorf("v2 read too much: %q", rest)
	}
	if _, err := readProxyHeader(strings.NewReader("GET / HTTP/1.1\r\n")); err == nil {
		t.Error("no header accepted")
	}
}

// The listener reads the header only from the proxies named.
func TestProxyListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pl := &ProxyListener{Listener: ln, From: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}}
	go func() {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err == nil {
			c.Write([]byte("PROXY TCP4 198.51.100.7 192.0.2.10 51000 443\r\nX"))
			c.Close()
		}
	}()
	c, err := pl.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.RemoteAddr().String() != "198.51.100.7:51000" {
		t.Errorf("remote %s", c.RemoteAddr())
	}
	pl.Close()
}

func TestLimits(t *testing.T) {
	now := time.Unix(1000, 0)
	l := &limiter{now: func() time.Time { return now }}
	a, other := netip.MustParseAddr("203.0.113.5"), netip.MustParseAddr("203.0.113.6")
	for i := 0; i < Burst; i++ {
		if !l.allow(a) {
			t.Fatalf("refused at %d", i)
		}
	}
	if l.allow(a) {
		t.Error("past the burst, allowed")
	}
	if !l.allow(other) {
		t.Error("another address refused")
	}
	now = now.Add(time.Second)
	for i := 0; i < Rate; i++ {
		if !l.allow(a) {
			t.Fatalf("a second later, refused at %d", i)
		}
	}
	if l.allow(a) {
		t.Error("past a second's refill, allowed")
	}
}

// Routes that need a session: refused at the gateway without the
// backend's signature of the login cookie; public routes don't need it.
func TestSessionFilter(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	b := &fakeBackend{connected: true, pub: pub}
	h := Handler(b, "1.0", nil, nil, quiet)
	for name, c := range map[string]struct {
		cookie string
		passes bool
	}{
		"nothing":               {"", false},
		"a login alone":         {"overfin-login=L", false},
		"a forged signature":    {"overfin-login=L; overfin-edge=" + edge.Sign(otherKey, "L"), false},
		"another login's":       {"overfin-login=M; overfin-edge=" + edge.Sign(key, "L"), false},
		"the real pair":         {"overfin-login=L; overfin-edge=" + edge.Sign(key, "L"), true},
		"the real pair, secure": {"__Host-overfin-login=L; __Host-overfin-edge=" + edge.Sign(key, "L"), true},
	} {
		b.seen = nil
		rec := do(h, "GET", "/api/v1/me", "203.0.113.5:1", "Cookie", c.cookie)
		if passed := len(b.seen) == 1; passed != c.passes {
			t.Errorf("%s: passed %v (%d)", name, passed, rec.Code)
		}
		if !c.passes && rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	// Public routes and pages pass without one.
	for _, path := range []string{"/api/v1/health", "/signin", "/"} {
		b.seen = nil
		do(h, "GET", path, "203.0.113.5:1")
		if len(b.seen) != 1 {
			t.Errorf("%s without a session: refused", path)
		}
	}
	b.seen = nil
	do(h, "POST", "/api/v1/federation/titles", "203.0.113.5:1")
	if len(b.seen) != 1 {
		t.Error("a paired server's request (signed, no session) refused")
	}
}

// eventsBackend streams one revocation, then answers the rest as fakeBackend.
type eventsBackend struct {
	fakeBackend
	revoke string
	access bool // send an access event too
}

func (e *eventsBackend) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/link/v1/events" {
		sum := sha256.Sum256([]byte(e.revoke))
		line := `{"type":"revoked","seq":7,"hash":"` + hex.EncodeToString(sum[:]) + `","expires":` + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) + "}\n"
		if e.access {
			line += `{"type":"access"}` + "\n"
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(line)), Request: r}, nil
	}
	return e.fakeBackend.RoundTrip(r)
}

func TestRevocations(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	b := &eventsBackend{fakeBackend: fakeBackend{connected: true, pub: pub}, revoke: "ended"}
	rv := &Revocations{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rv.Follow(ctx, b, quiet)
	deadline := time.Now().Add(5 * time.Second)
	for !rv.Revoked("ended") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	h := Handler(b, "1.0", rv, nil, quiet)
	if rec := do(h, "GET", "/api/v1/me", "203.0.113.5:1", "Cookie", "overfin-login=ended; overfin-edge="+edge.Sign(key, "ended")); rec.Code != http.StatusUnauthorized {
		t.Errorf("an ended login with its signature: %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/v1/me", "203.0.113.5:1", "Cookie", "overfin-login=live; overfin-edge="+edge.Sign(key, "live")); rec.Code != http.StatusOK {
		t.Errorf("another login: %d", rec.Code)
	}
}

// The cache forgets its access answers each time the events stream starts
// (a change during a break isn't missed), and on each access event.
func TestAccessEvents(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	b := &eventsBackend{fakeBackend: fakeBackend{connected: true, pub: pub}, revoke: "x", access: true}
	var mu sync.Mutex
	forgot := 0
	rv := &Revocations{Access: func() { mu.Lock(); forgot++; mu.Unlock() }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rv.Follow(ctx, b, quiet)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := forgot
		mu.Unlock()
		if n >= 2 {
			return // the stream's start, then its access event
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("forgot %d times", forgot)
}

// headersBackend keeps the headers of the last request it got.
type headersBackend struct {
	fakeBackend
	got http.Header
}

func (h *headersBackend) RoundTrip(r *http.Request) (*http.Response, error) {
	h.got = r.Header.Clone()
	return h.fakeBackend.RoundTrip(r)
}

// What a visitor sends in the gateway's or a proxy's name never reaches the
// backend; the gateway's own headers do.
func TestInternalHeadersDropped(t *testing.T) {
	b := &headersBackend{fakeBackend: fakeBackend{connected: true}}
	h := Handler(b, "1.0", nil, nil, quiet)
	rec := do(h, "GET", "/", "203.0.113.5:4000",
		"Overfin-Check", "1", "Overfin-Client-Addr", "198.51.100.66:1", "X-Real-Ip", "198.51.100.67",
		"Forwarded", "for=198.51.100.68", "X-Forwarded-Host", "evil.example", "X-Forwarded-Proto", "https")
	if rec.Code != 200 {
		t.Fatalf("page: %d", rec.Code)
	}
	for _, name := range []string{"Overfin-Check", "X-Real-Ip", "Forwarded", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-For"} {
		if v := b.got.Get(name); v != "" {
			t.Errorf("%s reached the backend: %q", name, v)
		}
	}
	if v := b.got.Get(link.ClientAddr); v != "203.0.113.5:4000" {
		t.Errorf("the visitor's address: %q", v)
	}
	// Over TLS, the gateway says so itself.
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.5:4000"
	req.TLS = &tls.ConnectionState{}
	h.ServeHTTP(httptest.NewRecorder(), req)
	if v := b.got.Get("X-Forwarded-Proto"); v != "https" {
		t.Errorf("over TLS: %q", v)
	}
}

// PROXY headers that name no visitor keep the proxy's own address: v1
// UNKNOWN, v2 LOCAL, a v2 family other than TCP.
func TestProxyHeadersWithoutVisitor(t *testing.T) {
	if a, err := readProxyHeader(strings.NewReader("PROXY UNKNOWN\r\nrest")); err != nil || a != nil {
		t.Errorf("v1 UNKNOWN: %v %v", a, err)
	}
	v2 := func(command, family byte, body []byte) []byte {
		var h bytes.Buffer
		h.Write(v2Signature)
		h.Write([]byte{0x20 | command, family})
		binary.Write(&h, binary.BigEndian, uint16(len(body)))
		h.Write(body)
		return h.Bytes()
	}
	if a, err := readProxyHeader(bytes.NewReader(v2(0, 0, nil))); err != nil || a != nil {
		t.Errorf("v2 LOCAL: %v %v", a, err)
	}
	if a, err := readProxyHeader(bytes.NewReader(v2(1, 0x31, make([]byte, 216)))); err != nil || a != nil {
		t.Errorf("v2 UNIX: %v %v", a, err)
	}
}

// Past its limit, an address gets 429, told when to try again.
func TestLimitAnswer(t *testing.T) {
	h := Limit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	var rec *httptest.ResponseRecorder
	for range 500 {
		rec = httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "203.0.113.9:1"
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			break
		}
	}
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "5" {
		t.Errorf("past the limit: %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
}
