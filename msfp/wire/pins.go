package wire

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Certificates between servers (core section 3.2): a server's key vouches for
// the certificates its address serves, named by pin in its signed hello.
// A connection to a peer is accepted when its certificate is publicly
// trusted, or is one the peer named.

// MaxPins caps the certificates a server may name.
const MaxPins = 8

// Pin is how a certificate is named between servers: base64url, without
// padding, of the SHA-256 of its public key (its SubjectPublicKeyInfo).
func Pin(c *x509.Certificate) string {
	sum := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	return b64.EncodeToString(sum[:])
}

// ValidPin reports whether s is a pin as Pin writes it.
func ValidPin(s string) bool {
	b, err := b64.DecodeString(s)
	return err == nil && len(b) == sha256.Size && b64.EncodeToString(b) == s
}

// CleanPins keeps the first MaxPins valid pins, each once, in the order
// sent (core section 3.2), then sorted, so that two lists compare as sets.
func CleanPins(in []string) []string {
	var out []string
	for _, p := range in {
		if len(out) == MaxPins {
			break
		}
		if ValidPin(p) && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

// ErrUntrustedCertificate is returned for a connection to a peer whose
// certificate is neither publicly trusted nor one the peer named.
var ErrUntrustedCertificate = errors.New("federation: the other server's certificate is neither publicly trusted nor one it named")

// VerifyPeer checks a peer's certificate: trusted by roots (nil: the
// system's) for host, or, its key, one of pins. With anyCert, any
// certificate passes.
func VerifyPeer(roots *x509.CertPool, now func() time.Time, host string, pins []string, anyCert bool) func(tls.ConnectionState) error {
	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return ErrUntrustedCertificate
		}
		if anyCert {
			return nil
		}
		// Only the first certificate's: the handshake proves the server
		// holds its private key, not any other's in the chain.
		leaf := cs.PeerCertificates[0]
		if slices.Contains(pins, Pin(leaf)) {
			return nil
		}
		opts := x509.VerifyOptions{Roots: roots, DNSName: host, Intermediates: x509.NewCertPool()}
		if now != nil {
			opts.CurrentTime = now()
		}
		for _, c := range cs.PeerCertificates[1:] {
			opts.Intermediates.AddCert(c)
		}
		if _, err := leaf.Verify(opts); err != nil {
			return fmt.Errorf("%w: %v", ErrUntrustedCertificate, err)
		}
		return nil
	}
}
