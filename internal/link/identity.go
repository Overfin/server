// Package link is the connection between Overfin's backend and its
// gateway. The backend connects out to the gateway and keeps the
// connection open; on it, the roles flip: the gateway sends HTTP/2
// requests, the backend answers them. Nothing is opened where the backend
// runs.
//
// Each side has an Ed25519 key of its own. They pair once, with a one-time
// code the operator gives both: each proves it knows the code, bound to
// both keys, and from then on each accepts only the other's key (mutual
// TLS, no certificate authority). The TLS connection uses the ALPN name
// "overfin-link", so it can share the gateway's own HTTPS port with
// visitors.
package link

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base32"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// ALPN is the TLS protocol name of the link.
const ALPN = "overfin-link"

// Identity is one side's own key.
type Identity struct {
	key  ed25519.PrivateKey
	cert tls.Certificate
}

// LoadIdentity reads the key at path, or makes one and saves it there
// (readable by its owner only) when there's none yet.
func LoadIdentity(path string) (*Identity, error) {
	b, err := os.ReadFile(path)
	var key ed25519.PrivateKey
	switch {
	case errors.Is(err, os.ErrNotExist):
		_, key, err = ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		if err := writeFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		block, _ := pem.Decode(b)
		if block == nil {
			return nil, fmt.Errorf("%s isn't a key", path)
		}
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		var ok bool
		if key, ok = k.(ed25519.PrivateKey); !ok {
			return nil, fmt.Errorf("%s isn't an Ed25519 key", path)
		}
	}
	return newIdentity(key)
}

func newIdentity(key ed25519.PrivateKey) (*Identity, error) {
	// A certificate only carries the key through TLS: nobody checks its
	// name or dates, the other side checks the key itself.
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Unix(0, 0),
		NotAfter:     time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, err
	}
	return &Identity{key: key, cert: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}}, nil
}

// Public is the identity's public key.
func (id *Identity) Public() ed25519.PublicKey { return id.key.Public().(ed25519.PublicKey) }

// Fingerprint is a short name for a public key, to show and log.
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:8])
}

// peerKey is the Ed25519 key the other side presented.
func peerKey(cs tls.ConnectionState) (ed25519.PublicKey, error) {
	if len(cs.PeerCertificates) == 0 {
		return nil, errors.New("no key presented")
	}
	pub, ok := cs.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("not an Ed25519 key")
	}
	return pub, nil
}

// writeFile writes a file whole (through a temporary file renamed), for
// its owner only.
func writeFile(path string, b []byte) error {
	tmp := path + ".new"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
