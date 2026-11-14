package link

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// backendHandler answers the gateway's requests in the tests: /hello, and
// /bytes?n= with n bytes.
var backendHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/hello":
		io.WriteString(w, "hello from the backend")
	case "/bytes":
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		buf := make([]byte, 32<<10)
		for n > 0 {
			k := min(n, len(buf))
			if _, err := w.Write(buf[:k]); err != nil {
				return
			}
			n -= k
		}
	default:
		http.NotFound(w, r)
	}
})

// gatewayOn starts a gateway with its own listener; code is what the
// operator set there.
func gatewayOn(t *testing.T, dir, version string, code *string) (*Gateway, string) {
	t.Helper()
	g, err := NewGateway(dir, version, func() string { return *code }, quiet)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go g.Listen(ctx, addr)
	time.Sleep(50 * time.Millisecond)
	return g, addr
}

// runBackend runs a backend until the test ends (or stop is called).
func runBackend(t *testing.T, dir, version string) (*Backend, context.CancelFunc) {
	t.Helper()
	b, err := NewBackend(dir, version, backendHandler, quiet)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go b.Run(ctx)
	return b, cancel
}

func waitConnected(t *testing.T, g *Gateway, want bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for g.Status().Connected != want {
		if time.Now().After(deadline) {
			t.Fatalf("connected: %v, wanted %v (%+v)", g.Status().Connected, want, g.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func get(t *testing.T, g *Gateway, path string) string {
	t.Helper()
	resp, err := g.Client().Get("https://backend" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func pairNow(t *testing.T, b *Backend, addr string) string {
	t.Helper()
	code, err := b.Pair(addr, "")
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// Pairing, then the gateway's requests answered by the backend; the next
// connection goes on the pinned keys, without a code.
func TestPairAndReconnect(t *testing.T) {
	gdir, bdir := t.TempDir(), t.TempDir()
	code := ""
	g, addr := gatewayOn(t, gdir, "1.0", &code)
	if _, err := g.Client().Get("https://backend/hello"); err == nil {
		t.Fatal("a request with no backend connected went through")
	}
	b, stop := runBackend(t, bdir, "1.0")
	code = pairNow(t, b, addr)
	waitConnected(t, g, true)
	if got := get(t, g, "/hello"); got != "hello from the backend" {
		t.Fatalf("got %q", got)
	}
	if st := g.Status(); !st.Paired || st.Backend != Fingerprint(b.Public()) {
		t.Errorf("status %+v", st)
	}

	// The backend stops, comes back: connected again, no code needed.
	stop()
	waitConnected(t, g, false)
	code = ""
	runBackend(t, bdir, "1.0")
	waitConnected(t, g, true)
	if got := get(t, g, "/hello"); got != "hello from the backend" {
		t.Fatalf("after reconnecting: %q", got)
	}
}

// A code used once doesn't pair a second backend.
func TestCodeUsedOnce(t *testing.T) {
	code := ""
	g, addr := gatewayOn(t, t.TempDir(), "1.0", &code)
	b, _ := runBackend(t, t.TempDir(), "1.0")
	code = pairNow(t, b, addr)
	waitConnected(t, g, true)

	// Another backend, given the same code by hand.
	other, err := NewBackend(t.TempDir(), "1.0", backendHandler, quiet)
	if err != nil {
		t.Fatal(err)
	}
	p := pairing{Address: addr, ServerName: "127.0.0.1", Code: code}
	other.save(p)
	err = other.connect(context.Background(), p)
	if err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("second use of a code: %v", err)
	}
	if g.Status().Backend != Fingerprint(b.Public()) {
		t.Error("the pairing changed")
	}

	// The backend pairs again with a new code; the first one, set again on
	// the gateway, is still refused: every code used is remembered.
	first := code
	code = pairNow(t, b, addr)
	waitConnected(t, g, true)
	code = first
	other.save(p)
	if err := other.connect(context.Background(), p); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("an older code used again: %v", err)
	}
}

// The wrong code, an unpaired backend, another version: refused, with
// what's wrong said.
func TestRefusals(t *testing.T) {
	code := ""
	g, addr := gatewayOn(t, t.TempDir(), "1.0", &code)

	b, err := NewBackend(t.TempDir(), "1.0", backendHandler, quiet)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := NewCode()
	code = other
	p := pairing{Address: addr, ServerName: "127.0.0.1"}
	p.Code, _ = NewCode()
	if err := b.connect(context.Background(), p); err == nil || !strings.Contains(err.Error(), "another pairing code") {
		t.Errorf("wrong code: %v", err)
	}
	if g.Status().Paired {
		t.Error("paired with the wrong code")
	}

	// Pinned on a gateway it never paired with: that gateway refuses it.
	p = pairing{Address: addr, ServerName: "127.0.0.1", Gateway: fmt.Sprintf("%x", g.Public())}
	if err := b.connect(context.Background(), p); err == nil || !strings.Contains(err.Error(), "isn't paired") {
		t.Errorf("unpaired backend: %v", err)
	}

	// Another version, even with the right code.
	b2, err := NewBackend(t.TempDir(), "2.0", backendHandler, quiet)
	if err != nil {
		t.Fatal(err)
	}
	p = pairing{Address: addr, ServerName: "127.0.0.1", Code: code}
	if err := b2.connect(context.Background(), p); err == nil || !strings.Contains(err.Error(), "version 1.0") {
		t.Errorf("another version: %v", err)
	}
	if st := g.Status(); st.Paired || st.Mismatch != "2.0" {
		t.Errorf("status after another version: %+v", st)
	}
	// A backend of another version that stopped trying no longer counts:
	// visitors aren't told it's being updated forever.
	g.mu.Lock()
	g.mismatchAt = time.Now().Add(-mismatchFor - time.Second)
	g.mu.Unlock()
	if st := g.Status(); st.Mismatch != "" {
		t.Errorf("status long after another version: %+v", st)
	}
}

// A gateway whose key isn't the pinned one (replaced, or someone else at
// that address) is refused by the backend.
func TestGatewayKeyChanged(t *testing.T) {
	code := ""
	g, addr := gatewayOn(t, t.TempDir(), "1.0", &code)
	bdir := t.TempDir()
	b, stop := runBackend(t, bdir, "1.0")
	code = pairNow(t, b, addr)
	waitConnected(t, g, true)
	stop()

	// Another gateway at another address; the backend's pairing pointed
	// there, without a new code.
	_, addr2 := gatewayOn(t, t.TempDir(), "1.0", &code)
	p, _ := b.load()
	p.Address = addr2
	if err := b.connect(context.Background(), p); err == nil || !strings.Contains(err.Error(), "isn't the paired one") {
		t.Fatalf("changed key: %v", err)
	}
}

// Large answers, many at once, arrive whole.
func TestParallelTransfers(t *testing.T) {
	code := ""
	g, addr := gatewayOn(t, t.TempDir(), "1.0", &code)
	b, _ := runBackend(t, t.TempDir(), "1.0")
	code = pairNow(t, b, addr)
	waitConnected(t, g, true)
	const n, size = 8, 16 << 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := g.Client().Get("https://backend/bytes?n=" + strconv.Itoa(size))
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			got, err := io.Copy(io.Discard, resp.Body)
			if err != nil || got != size {
				errs <- fmt.Errorf("%d bytes, %v", got, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// The gateway's own HTTPS port: visitors get the site, the backend's
// connection the link, told apart by ALPN.
func TestSharedPort(t *testing.T) {
	code := ""
	g, err := NewGateway(t.TempDir(), "1.0", func() string { return code }, quiet)
	if err != nil {
		t.Fatal(err)
	}
	site := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "the site") })
	srv := &http.Server{Handler: site, TLSConfig: &tls.Config{Certificates: []tls.Certificate{siteCert(t)}}}
	g.Share(srv)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.ServeTLS(ln, "", "")
	t.Cleanup(func() { srv.Close() })
	addr := ln.Addr().String()

	visitor := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := visitor.Get("https://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "the site" {
		t.Fatalf("visitor got %q", body)
	}

	b, _ := runBackend(t, t.TempDir(), "1.0")
	code = pairNow(t, b, addr)
	waitConnected(t, g, true)
	if got := get(t, g, "/hello"); got != "hello from the backend" {
		t.Fatalf("over the shared port: %q", got)
	}
}

// siteCert is a certificate for the site's side of the shared port.
func siteCert(t *testing.T) tls.Certificate {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestCodes(t *testing.T) {
	c, err := NewCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 39 || strings.Count(c, "-") != 7 {
		t.Errorf("code %q", c)
	}
	a, err := codeBytes(strings.ToLower(strings.ReplaceAll(c, "-", " ")))
	b, _ := codeBytes(c)
	if err != nil || string(a) != string(b) {
		t.Error("a code typed differently isn't the same code")
	}
	if _, err := codeBytes("ABCD-EFGH"); err == nil {
		t.Error("a short code accepted")
	}
}

// The visitor's address the gateway gives becomes the request's own at the
// backend, and the header is gone before the backend's code sees it.
func TestClientAddr(t *testing.T) {
	code := ""
	g, addr := gatewayOn(t, t.TempDir(), "1.0", &code)
	seen := make(chan [2]string, 2)
	b, err := NewBackend(t.TempDir(), "1.0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- [2]string{r.RemoteAddr, r.Header.Get(ClientAddr)}
	}), quiet)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	code = pairNow(t, b, addr)
	go b.Run(ctx)
	waitConnected(t, g, true)

	req, _ := http.NewRequest("GET", "https://backend/x", nil)
	req.Header.Set(ClientAddr, "203.0.113.7:51234")
	resp, err := g.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := <-seen; got[0] != "203.0.113.7:51234" || got[1] != "" {
		t.Errorf("with the header: %q", got)
	}
	// Not an address: ignored, and still taken off.
	req.Header.Set(ClientAddr, "not an address")
	resp, err = g.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := <-seen; got[0] == "not an address" || got[1] != "" {
		t.Errorf("with a bad header: %q", got)
	}
}

// The backend reaches another server through its gateway: TLS end to end,
// the traffic counted at the gateway; an address the backend didn't ask
// for is refused there.
func TestEgress(t *testing.T) {
	// The test's servers are on loopback, which a gateway never dials.
	was := unreached
	unreached = func(netip.Addr) bool { return false }
	t.Cleanup(func() { unreached = was })
	code := ""
	g, addr := gatewayOn(t, t.TempDir(), "1.0", &code)
	b, _ := runBackend(t, t.TempDir(), "1.0")
	code = pairNow(t, b, addr)
	waitConnected(t, g, true)

	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "a paired server says hello")
	}))
	defer peer.Close()
	peerAddr := strings.TrimPrefix(peer.URL, "https://")
	tr := peer.Client().Transport.(*http.Transport).Clone()
	tr.DialContext = b.DialEgress
	resp, err := (&http.Client{Transport: tr}).Get(peer.URL + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "a paired server says hello" {
		t.Fatalf("through the gateway: %q", body)
	}
	tr.CloseIdleConnections()
	deadline := time.Now().Add(5 * time.Second)
	for g.EgressUsage()[peerAddr].Received == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if u := g.EgressUsage()[peerAddr]; u.Sent == 0 || u.Received == 0 {
		t.Errorf("not counted: %+v", u)
	}

	if _, err := b.dialEgress(context.Background(), "tcp", "192.0.2.1:443", false); err == nil || !strings.Contains(err.Error(), "not-allowed") {
		t.Errorf("an address the backend didn't ask for: %v", err)
	}
}

// A gateway never dials its own machine, link-local addresses (a cloud's
// metadata service), unspecified or multicast ones for its backend; LAN and
// public addresses, it does.
func TestUnreached(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1": true, "::1": true, "169.254.169.254": true, "fe80::1": true, "0.0.0.0": true,
		"224.0.0.1": true, "::ffff:127.0.0.1": true,
		"10.1.2.3": false, "192.168.1.5": false, "8.8.8.8": false, "2001:db8::1": false,
	} {
		if got := unreached(netip.MustParseAddr(addr).Unmap()); got != want {
			t.Errorf("%s: %v, want %v", addr, got, want)
		}
	}
	if _, ok := egressTarget("127.0.0.1:443"); ok {
		t.Error("loopback taken")
	}
	if target, ok := egressTarget("10.1.2.3:8443"); !ok || target != "10.1.2.3:8443" {
		t.Errorf("a LAN address: %q %v", target, ok)
	}
}

// Waits between tries vary by a fifth either way.
func TestJittered(t *testing.T) {
	lo, hi := time.Hour, time.Duration(0)
	for range 200 {
		d := jittered(10 * time.Second)
		lo, hi = min(lo, d), max(hi, d)
	}
	if lo < 8*time.Second || hi > 12*time.Second || hi-lo < time.Second {
		t.Errorf("between %v and %v", lo, hi)
	}
}

// A connection that offers the link's protocol but not its preamble is
// refused: a line that isn't one, a short one, another tag, an over-long
// one.
func TestPreambleRefused(t *testing.T) {
	code := ""
	_, addr := gatewayOn(t, t.TempDir(), "1.0", &code)
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	id, err := newIdentity(key)
	if err != nil {
		t.Fatal(err)
	}
	try := func(line string) string {
		c, err := tls.Dial("tcp", addr, &tls.Config{Certificates: []tls.Certificate{id.cert}, NextProtos: []string{ALPN},
			InsecureSkipVerify: true, MinVersion: tls.VersionTLS13})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		io.WriteString(c, line)
		b, _ := io.ReadAll(c)
		return string(b)
	}
	for _, line := range []string{"hello\n", "OVFLINK1 link\n", "OVFLINK2 link 1.0\n", "OVFLINK1 dance 1.0\n", "OVFLINK1 pair 1.0 nothex\n"} {
		if got := try(line); got != "NO preamble\n" {
			t.Errorf("%q: %q", line, got)
		}
	}
	if got := try("OVFLINK1 link 1.0 " + strings.Repeat("x", 600) + "\n"); strings.HasPrefix(got, "OK") {
		t.Errorf("an over-long line: %q", got)
	}
	if got := try("OVFLINK1 link 9.9\n"); got != "NO version 1.0\n" {
		t.Errorf("another version: %q", got)
	}
	if got := try("OVFLINK1 link 1.0\n"); got != "NO not-paired\n" {
		t.Errorf("a key that isn't paired: %q", got)
	}
}

// The same backend connecting again (its former connection lost to it):
// the newest connection is the one used.
func TestSecondConnection(t *testing.T) {
	code := ""
	g, addr := gatewayOn(t, t.TempDir(), "1.0", &code)
	bdir := t.TempDir()
	b, _ := runBackend(t, bdir, "1.0")
	code = pairNow(t, b, addr)
	waitConnected(t, g, true)
	first := g.Status().Since
	again, err := NewBackend(bdir, "1.0", backendHandler, quiet)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := again.load()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go again.connect(ctx, p)
	deadline := time.Now().Add(5 * time.Second)
	for !g.Status().Since.After(first) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if st := g.Status(); !st.Connected || !st.Since.After(first) {
		t.Fatalf("not replaced: %+v", st)
	}
	if got := get(t, g, "/hello"); got != "hello from the backend" {
		t.Errorf("after the replacement: %q", got)
	}
}

// The gateway's own question (Overfin-Check): the backend runs the
// request, answers its status and headers only, 204 for 200.
func TestCheckOnly(t *testing.T) {
	h := checkOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(CheckOnly) != "" {
			t.Error("the header reached the site")
		}
		if r.URL.Path == "/no" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", "5")
		io.WriteString(w, "image")
	}))
	ask := func(path string, check bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if check {
			r.Header.Set(CheckOnly, "1")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	if rec := ask("/yes", true); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "" {
		t.Errorf("a yes: %d, %d bytes, length %q", rec.Code, rec.Body.Len(), rec.Header().Get("Content-Length"))
	}
	if rec := ask("/no", true); rec.Code != http.StatusNotFound || rec.Body.Len() != 0 {
		t.Errorf("a no: %d, %d bytes", rec.Code, rec.Body.Len())
	}
	if rec := ask("/yes", false); rec.Code != http.StatusOK || rec.Body.String() != "image" {
		t.Errorf("a visitor's request: %d %q", rec.Code, rec.Body.String())
	}
}

// Egress refused: a backend of another version, a key the gateway doesn't
// know, an address that doesn't answer; an address asked for counts a
// minute only.
func TestEgressRefusals(t *testing.T) {
	was := unreached
	unreached = func(netip.Addr) bool { return false }
	t.Cleanup(func() { unreached = was })
	code := ""
	g, addr := gatewayOn(t, t.TempDir(), "1.0", &code)
	bdir := t.TempDir()
	b, _ := runBackend(t, bdir, "1.0")
	code = pairNow(t, b, addr)
	waitConnected(t, g, true)
	ctx := context.Background()

	newer, err := NewBackend(bdir, "2.0", backendHandler, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newer.dialEgress(ctx, "tcp", "192.0.2.1:443", true); err == nil || !strings.Contains(err.Error(), "version") {
		t.Errorf("another version: %v", err)
	}
	stranger, err := NewBackend(t.TempDir(), "1.0", backendHandler, quiet)
	if err != nil {
		t.Fatal(err)
	}
	stranger.save(pairing{Address: addr, ServerName: "127.0.0.1", Gateway: fmt.Sprintf("%x", []byte(g.Public()))})
	if _, err := stranger.dialEgress(ctx, "tcp", "192.0.2.1:443", true); err == nil || !strings.Contains(err.Error(), "not-paired") {
		t.Errorf("a key the gateway doesn't know: %v", err)
	}
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	gone := closed.Addr().String()
	closed.Close()
	if _, err := b.dialEgress(ctx, "tcp", gone, true); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("an address that doesn't answer: %v", err)
	}

	b.asked("192.0.2.9:443")
	if !b.meant("192.0.2.9:443") {
		t.Fatal("not meant right after")
	}
	b.egressMu.Lock()
	b.egress["192.0.2.9:443"] = time.Now().Add(-egressAsked - time.Second)
	b.egressMu.Unlock()
	if b.meant("192.0.2.9:443") {
		t.Error("still meant after a minute")
	}
}
