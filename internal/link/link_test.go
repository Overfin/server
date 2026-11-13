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
