package federation

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/overfin/server/internal/outbound"
	"github.com/overfin/server/internal/store"
)

// Certificates between servers (SECURITY.md rule 99). A server is its key,
// never its certificate, but the media and its ticket (/api/v1/stream/…)
// are protected by TLS alone. So a peer's key vouches for its
// certificate: in its signed hello, a server names the certificates its
// address serves, by pin. A connection to a peer is then accepted when its
// certificate is publicly trusted, as for any site, or is one the peer
// named. Before the peer has named any, a certificate of its own is
// accepted for pair and hello only: their answers are checked by key, and
// carry nothing secret. Every other call, and the media, wants a trusted
// or named one.

// maxPins caps the certificates a server may name.
const maxPins = 8

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

// cleanPins keeps the valid pins, each once, sorted, at most maxPins.
func cleanPins(in []string) []string {
	var out []string
	for _, p := range in {
		if ValidPin(p) && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	if len(out) > maxPins {
		out = out[:maxPins]
	}
	return out
}

// ErrUntrustedCertificate is returned for a connection to a peer whose
// certificate is neither publicly trusted nor one the peer named.
var ErrUntrustedCertificate = errors.New("federation: the other server's certificate is neither publicly trusted nor one it named")

// verifyPeer checks a peer's certificate: trusted by roots (nil: the
// system's) for host, or, its key, one of pins. With anyCert, any
// certificate passes.
func verifyPeer(roots *x509.CertPool, now func() time.Time, host string, pins []string, anyCert bool) func(tls.ConnectionState) error {
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

// peerConns are the TLS settings and client made for one peer, as it was.
type peerConns struct {
	sig    string // its address and pins
	tls    *tls.Config
	client *http.Client
}

// anyCertificate is conns' key for pair and hello's client.
const anyCertificate = "*"

// baseTransport is what every connection to peers starts from: the HTTP
// client's transport, with its TLS settings (a test's own roots).
func (s *Service) baseTransport() (*http.Transport, bool) {
	rt := s.HTTP.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	t, ok := rt.(*http.Transport)
	return t, ok
}

// conns gives the TLS settings and client for connections to p, or, with
// anyCert, to anyone (pair and hello). They're made again when p's address
// or pins change.
func (s *Service) conns(p *store.Peer, anyCert bool) *peerConns {
	key, sig := anyCertificate, ""
	host := ""
	if !anyCert {
		key, sig = p.ID, p.Address+" "+strings.Join(p.Certificates, " ")
		if u, err := url.Parse(p.Address); err == nil {
			host = u.Hostname()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.peerConns[key]; c != nil && c.sig == sig {
		return c
	}
	t, ok := s.baseTransport()
	if !ok {
		// A client of a test's own making: as it is.
		return &peerConns{sig: sig, client: s.HTTP}
	}
	var cfg *tls.Config
	if t.TLSClientConfig != nil {
		cfg = t.TLSClientConfig.Clone()
	} else {
		cfg = &tls.Config{}
	}
	// Checked by VerifyConnection instead, which Go runs either way.
	cfg.InsecureSkipVerify = true
	cfg.VerifyConnection = verifyPeer(cfg.RootCAs, cfg.Time, host, p.Certificates, anyCert)
	tr := t.Clone()
	tr.TLSClientConfig = cfg
	c := &peerConns{sig: sig, tls: cfg,
		client: &http.Client{Transport: tr, Timeout: s.HTTP.Timeout, CheckRedirect: s.HTTP.CheckRedirect}}
	if old := s.peerConns[key]; old != nil {
		if t, ok := old.client.Transport.(*http.Transport); ok {
			t.CloseIdleConnections()
		}
	}
	if s.peerConns == nil {
		s.peerConns = map[string]*peerConns{}
	}
	s.peerConns[key] = c
	return c
}

// TLSConfig is the TLS settings for fetching p's media: its certificate
// publicly trusted, or one it named. nil means the system's settings.
func (s *Service) TLSConfig(p *store.Peer) *tls.Config {
	return s.conns(p, false).tls
}

// Repin asks p, by hello, which certificates its address serves now,
// after err came of a connection to it: when its certificate wasn't one
// trusted or named. It gives p as it is then, and whether its pins
// changed, so that trying again may work.
func (s *Service) Repin(ctx context.Context, p *store.Peer, err error) (*store.Peer, bool) {
	if !errors.Is(err, ErrUntrustedCertificate) || p.ID == "" {
		return p, false
	}
	q, cerr := s.Check(ctx, p)
	if cerr != nil || q == nil {
		return p, false
	}
	return q, !slices.Equal(q.Certificates, p.Certificates)
}

// keepCertificates keeps the pins p named in a signed hello, replacing
// those it named before: none, if it named none.
func (s *Service) keepCertificates(ctx context.Context, p *store.Peer, named []string) error {
	pins := cleanPins(named)
	if slices.Equal(pins, p.Certificates) {
		return nil
	}
	if err := s.Store.SetPeerCertificates(ctx, p.ID, pins); err != nil {
		return err
	}
	s.Logger.Info("federation: a peer named other certificates", "name", p.Name, "count", len(pins))
	return nil
}

// probeEvery is how long what this server's address serves is taken as
// known.
const probeEvery = time.Minute

// probeAgain is how soon a reading that failed is tried again, when none
// ever worked.
var probeAgain = 5 * time.Second

// Certificates is what this server names of its own certificates in its
// hellos.
type Certificates struct {
	// Pins, when the operator set them, are named as they are, and
	// nothing is looked at.
	Pins []string

	mu   sync.Mutex
	seen []string
	at   time.Time
}

// For gives the pins to name for address: the operator's, or else the
// one of the certificate served there, as seen from here, looked at again
// after a minute (a renewed certificate is named at once). A look that
// fails keeps the last one seen; when there's none, the next look is
// after probeAgain.
func (c *Certificates) For(ctx context.Context, address string) ([]string, error) {
	return c.within(ctx, address, probeEvery)
}

// Fresh is For, looked at again unless the last look is under
// probeFresh old: what a hello answers, since a peer sends one when a
// call of its was refused this server's certificate (a renewal), and
// would otherwise get the old pin, and fail until its next try.
func (c *Certificates) Fresh(ctx context.Context, address string) ([]string, error) {
	return c.within(ctx, address, probeFresh)
}

// probeFresh is how recent a look a hello's answer takes as is.
const probeFresh = 2 * time.Second

func (c *Certificates) within(ctx context.Context, address string, maxAge time.Duration) ([]string, error) {
	if c == nil {
		return nil, nil
	}
	if len(c.Pins) > 0 {
		return c.Pins, nil
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < maxAge {
		return c.seen, nil
	}
	c.at = time.Now()
	pin, err := probe(ctx, u)
	if err != nil {
		if c.seen == nil {
			// Never read yet (a reverse proxy starting after Overfin):
			// tried again soon, rather than naming none for a minute,
			// which has peers drop the pins they had.
			c.at = c.at.Add(probeAgain - probeEvery)
		}
		return c.seen, err
	}
	c.seen = []string{pin}
	return c.seen, nil
}

// probe connects to this server's own address, as a peer would, and
// gives the pin of the certificate it is served there: whatever stands in
// front of Overfin (a reverse proxy) serves it.
func probe(ctx context.Context, u *url.URL) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	port := u.Port()
	if port == "" {
		port = "443"
	}
	raw, err := outbound.DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return "", err
	}
	defer raw.Close()
	// Not checked: what is served here is what peers are told of, and
	// their connections check it against that.
	conn := tls.Client(raw, &tls.Config{ServerName: u.Hostname(), InsecureSkipVerify: true})
	if err := conn.HandshakeContext(ctx); err != nil {
		return "", err
	}
	defer conn.Close()
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", errors.New("no certificate")
	}
	return Pin(certs[0]), nil
}
