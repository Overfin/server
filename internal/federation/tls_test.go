package federation

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// testCert makes a certificate for host, signed by parent's key (itself
// when parent is nil), and its key.
func testCert(t *testing.T, host string, ca bool, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), BasicConstraintsValid: true, IsCA: ca}
	if ca {
		tmpl.KeyUsage = x509.KeyUsageCertSign
	} else {
		tmpl.DNSNames = []string{host}
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	if parent == nil {
		parent, parentKey = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return c, key
}

// A peer's certificate passes when trusted for its host, or when its own
// key is one the peer named; a name of another certificate in the chain
// (a private authority's) counts for nothing, since the handshake proves
// only the first one's key.
func TestVerifyPeer(t *testing.T) {
	ca, caKey := testCert(t, "Private CA", true, nil, nil)
	leaf, _ := testCert(t, "a.example.org", false, ca, caKey)
	own, _ := testCert(t, "a.example.org", false, nil, nil)
	trusted := x509.NewCertPool()
	trusted.AddCert(ca)
	none := x509.NewCertPool()
	chain := []*x509.Certificate{leaf, ca}
	cases := []struct {
		name    string
		roots   *x509.CertPool
		host    string
		pins    []string
		anyCert bool
		certs   []*x509.Certificate
		ok      bool
	}{
		{"trusted", trusted, "a.example.org", nil, false, chain, true},
		{"trusted, another host", trusted, "b.example.org", nil, false, chain, false},
		{"not trusted", none, "a.example.org", nil, false, chain, false},
		{"named", none, "a.example.org", []string{Pin(own)}, false, []*x509.Certificate{own}, true},
		{"named, whatever its names", none, "192.0.2.10", []string{Pin(own)}, false, []*x509.Certificate{own}, true},
		{"another named", none, "a.example.org", []string{Pin(leaf)}, false, []*x509.Certificate{own}, false},
		{"its authority named", none, "a.example.org", []string{Pin(ca)}, false, chain, false},
		{"any certificate", none, "a.example.org", nil, true, []*x509.Certificate{own}, true},
		{"none at all", none, "a.example.org", nil, true, nil, false},
	}
	for _, c := range cases {
		err := verifyPeer(c.roots, nil, c.host, c.pins, c.anyCert)(tls.ConnectionState{PeerCertificates: c.certs})
		if (err == nil) != c.ok || (err != nil && !errors.Is(err, ErrUntrustedCertificate)) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestCleanPins(t *testing.T) {
	own, _ := testCert(t, "a.example.org", false, nil, nil)
	pin := Pin(own)
	if len(pin) != 43 || !ValidPin(pin) || ValidPin(pin+"=") || ValidPin(pin[:42]) || ValidPin(strings.ToUpper(pin)+"x") {
		t.Fatalf("pin %q", pin)
	}
	var many []string
	for i := range 10 {
		c, _ := testCert(t, "a.example.org", false, nil, nil)
		many = append(many, Pin(c))
		if i == 0 {
			many = append(many, Pin(c), "not a pin")
		}
	}
	got := cleanPins(many)
	if len(got) != maxPins || !slices.IsSorted(got) || slices.Contains(got, "not a pin") {
		t.Fatalf("cleaned: %v", got)
	}
}

// What a server names of its own: the operator's pins, or the one its
// https address serves, nothing for http, and nothing when its address
// can't be reached from itself.
func TestOwnCertificates(t *testing.T) {
	ctx := context.Background()
	var none *Certificates
	if got, err := none.For(ctx, "https://a.example.org"); got != nil || err != nil {
		t.Fatalf("none: %v %v", got, err)
	}
	set := &Certificates{Pins: []string{"x"}}
	if got, _ := set.For(ctx, "https://a.example.org"); !slices.Equal(got, []string{"x"}) {
		t.Fatalf("the operator's: %v", got)
	}
	probed := &Certificates{}
	if got, err := probed.For(ctx, "http://192.0.2.10:8096"); got != nil || err != nil {
		t.Fatalf("over http: %v %v", got, err)
	}
	if got, err := probed.For(ctx, "https://127.0.0.1:1"); got != nil || err == nil {
		t.Fatalf("unreachable: %v %v", got, err)
	}
	a := newServerOn(t, "A", true)
	u, _ := url.Parse(a.url)
	cert := tlsCertOf(t, u.Host)
	if got, err := (&Certificates{}).For(ctx, a.url); err != nil || !slices.Equal(got, []string{Pin(cert)}) {
		t.Fatalf("served: %v %v", got, err)
	}
}

// tlsCertOf is the certificate host serves.
func tlsCertOf(t *testing.T, host string) *x509.Certificate {
	t.Helper()
	conn, err := tls.Dial("tcp", host, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0]
}

// Two servers whose certificates no public authority issued pair over
// HTTPS: pair and hello take any certificate, and each names its own in
// its hellos; every other call then takes only the one named. A server
// that stops naming it is no longer reached; one that names it again (a
// renewal) is, at the next call, which asks its hello again.
func TestCertificatesNamedInHello(t *testing.T) {
	ctx := context.Background()
	a, b := newServerOn(t, "A", true), newServerOn(t, "B", true)
	u, _ := url.Parse(a.url)
	pin := Pin(tlsCertOf(t, u.Host))
	code, _, _ := a.s.NewCode(ctx, "")
	aOnB, err := b.s.UseCode(ctx, code)
	if err != nil {
		t.Fatalf("pairing over HTTPS: %v", err)
	}
	if len(aOnB.Certificates) != 0 {
		t.Fatalf("named before any hello: %v", aOnB.Certificates)
	}
	// Strict before any hello: nothing named, not trusted.
	if _, err := a.s.TheirLibraries(ctx, onlyPeer(t, a)); err == nil {
		t.Fatal("libraries from B before it was accepted")
	}
	if _, err := a.s.Accept(ctx, onlyPeer(t, a).ID); err != nil {
		t.Fatal(err)
	}
	// A's hello named A's certificate to B, and B's answer B's to A.
	aOnB = onlyPeer(t, b)
	bOnA := onlyPeer(t, a)
	if aOnB.State != "paired" || !slices.Equal(aOnB.Certificates, []string{pin}) || !slices.Equal(bOnA.Certificates, []string{pin}) {
		t.Fatalf("after accepting: B's view %+v, A's view %+v", aOnB, bOnA)
	}
	if _, err := b.s.TheirLibraries(ctx, aOnB); err != nil {
		t.Fatalf("libraries over a named certificate: %v", err)
	}
	// A names none any more: B learns it at its next hello, and is
	// refused A's certificate from then on.
	a.s.Certificates = nil
	if aOnB, err = b.s.Check(ctx, aOnB); err != nil || len(aOnB.Certificates) != 0 {
		t.Fatalf("after A named none: %+v %v", aOnB, err)
	}
	if _, err := b.s.TheirLibraries(ctx, aOnB); !errors.Is(err, ErrUntrustedCertificate) {
		t.Fatalf("libraries with nothing named: %v", err)
	}
	// A names it again: B's next call asks A's hello, and goes through.
	a.s.Certificates = &Certificates{}
	if _, err := b.s.TheirLibraries(ctx, aOnB); err != nil {
		t.Fatalf("libraries after a renewal: %v", err)
	}
	if got := onlyPeer(t, b); !slices.Equal(got.Certificates, []string{pin}) {
		t.Fatalf("B's view after the renewal: %v", got.Certificates)
	}
}

// A first reading that fails (a reverse proxy starting after Overfin) is
// tried again soon, not a minute later: meanwhile, hellos name none, and
// peers drop the pins they had.
func TestOwnCertificatesFirstFailureRetriedSoon(t *testing.T) {
	ctx := context.Background()
	defer func(was time.Duration) { probeAgain = was }(probeAgain)
	probeAgain = 50 * time.Millisecond
	a := newServerOn(t, "A", true)
	u, _ := url.Parse(a.url)
	cert := tlsCertOf(t, u.Host)
	// The address first refuses connections, then serves.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	c := &Certificates{}
	if got, err := c.For(ctx, "https://"+addr); got != nil || err == nil {
		t.Fatalf("nothing served yet: %v %v", got, err)
	}
	ln, err = net.Listen("tcp", addr)
	if err != nil {
		t.Skip("the port was taken meanwhile")
	}
	defer ln.Close()
	go func() {
		for {
			in, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer in.Close()
				out, err := net.Dial("tcp", u.Host)
				if err != nil {
					return
				}
				defer out.Close()
				go io.Copy(out, in)
				io.Copy(in, out)
			}()
		}
	}()
	time.Sleep(100 * time.Millisecond)
	if got, err := c.For(ctx, "https://"+addr); err != nil || !slices.Equal(got, []string{Pin(cert)}) {
		t.Fatalf("served now: %v %v", got, err)
	}
}
