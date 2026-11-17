package msfp

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
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
		err := VerifyPeer(c.roots, nil, c.host, c.pins, c.anyCert)(tls.ConnectionState{PeerCertificates: c.certs})
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
	got := CleanPins(many)
	if len(got) != MaxPins || !slices.IsSorted(got) || slices.Contains(got, "not a pin") {
		t.Fatalf("cleaned: %v", got)
	}
}
