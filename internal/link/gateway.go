package link

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// ErrNoBackend is a request the gateway can't send: no backend is
// connected (or the connected one is of another version).
var ErrNoBackend = errors.New("the backend isn't connected")

// handshakeWait bounds a connection's TLS handshake and preamble.
const handshakeWait = 15 * time.Second

// Gateway is the gateway's side of the link: it accepts its backend's
// connection and sends requests on it.
type Gateway struct {
	dir     string
	version string
	code    func() string // the pairing code the operator gave, if any
	logger  *slog.Logger
	id      *Identity

	mu       sync.Mutex
	backend  ed25519.PublicKey // pinned once paired
	cc       *http.ClientConn  // the backend's connection, nil while none
	status   Status
	failures int              // wrong codes in a row, for the log
	usage    map[string]Usage // egress traffic, by address
	// mismatchAt is when a backend of another version last tried.
	mismatchAt time.Time
}

// mismatchFor is how long a backend of another version counts after its
// last try: it tries at least every 30 s while it's there.
const mismatchFor = 2 * time.Minute

// Status is what the gateway knows of its backend.
type Status struct {
	Paired         bool      `json:"paired"`
	Connected      bool      `json:"connected"`
	BackendVersion string    `json:"backendVersion,omitempty"`
	Mismatch       string    `json:"mismatch,omitempty"` // the backend's version, when it isn't the gateway's
	Since          time.Time `json:"since"`              // since when Connected is what it is
	Backend        string    `json:"backend,omitempty"`  // the pinned key's fingerprint
}

// NewGateway loads (or makes) the gateway's key and its pairing from dir.
// code gives the pairing code the operator set, read each time a backend
// asks to pair.
func NewGateway(dir, version string, code func() string, logger *slog.Logger) (*Gateway, error) {
	id, err := LoadIdentity(filepath.Join(dir, "gateway.key"))
	if err != nil {
		return nil, err
	}
	g := &Gateway{dir: dir, version: version, code: code, logger: logger, id: id}
	if b, err := os.ReadFile(filepath.Join(dir, "backend.pub")); err == nil {
		k, err := hex.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(k) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%s isn't a backend's key", filepath.Join(dir, "backend.pub"))
		}
		g.backend = k
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	g.status = Status{Paired: g.backend != nil, Since: time.Now()}
	if g.backend != nil {
		g.status.Backend = Fingerprint(g.backend)
	}
	return g, nil
}

// BackendKey is the paired backend's key, nil while unpaired: the gateway
// checks the backend's edge signatures with it (package edge).
func (g *Gateway) BackendKey() ed25519.PublicKey {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.backend
}

// Public is the gateway's own key.
func (g *Gateway) Public() ed25519.PublicKey { return g.id.Public() }

// Status is what the gateway knows of its backend now. A backend of
// another version that stopped trying no longer counts.
func (g *Gateway) Status() Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.status
	if time.Since(g.mismatchAt) > mismatchFor {
		st.Mismatch = ""
	}
	return st
}

// tlsConfig is the TLS side of a backend's connection: the gateway's own
// key, the backend's required (checked in the preamble).
func (g *Gateway) tlsConfig() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{g.id.cert},
		ClientAuth:   tls.RequireAnyClientCert,
		NextProtos:   []string{ALPN, EgressALPN},
		MinVersion:   tls.VersionTLS13,
	}
}

// Share has the gateway's own HTTPS server take the backend's connections
// too, told apart from visitors by their ALPN name: srv's TLS settings
// stay as they are for everyone else (Let's Encrypt's checks included).
func (g *Gateway) Share(srv *http.Server) {
	base := srv.TLSConfig
	prev := base.GetConfigForClient
	base.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		if slices.Contains(hello.SupportedProtos, ALPN) || slices.Contains(hello.SupportedProtos, EgressALPN) {
			return g.tlsConfig(), nil
		}
		if prev != nil {
			return prev(hello)
		}
		return nil, nil
	}
	if srv.TLSNextProto == nil {
		srv.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){}
	}
	srv.TLSNextProto[ALPN] = func(_ *http.Server, c *tls.Conn, _ http.Handler) { g.Serve(c) }
	srv.TLSNextProto[EgressALPN] = func(_ *http.Server, c *tls.Conn, _ http.Handler) { g.Serve(c) }
}

// Listen takes the backend's connections on a listener of their own (a
// gateway behind a proxy that handles its HTTPS, or without HTTPS), until
// ctx ends.
func (g *Gateway) Listen(ctx context.Context, addr string) error {
	ln, err := tls.Listen("tcp", addr, g.tlsConfig())
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go g.Serve(c.(*tls.Conn))
	}
}

// Serve runs one backend connection: the handshake and preamble, then
// HTTP/2 with the gateway as the client, until the connection ends.
func (g *Gateway) Serve(c *tls.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(handshakeWait))
	if err := c.Handshake(); err != nil {
		return
	}
	cs := c.ConnectionState()
	pub, err := peerKey(cs)
	if err != nil {
		return
	}
	switch cs.NegotiatedProtocol {
	case EgressALPN:
		g.serveEgress(c, pub)
		return
	case ALPN:
	default:
		return
	}
	reply, ok := g.preamble(c, pub)
	if _, err := io.WriteString(c, reply+"\n"); err != nil || !ok {
		return
	}
	c.SetDeadline(time.Time{})
	cc, err := clientConn(context.Background(), c)
	if err != nil {
		g.logger.Warn("link: the backend's connection failed", "err", err)
		return
	}
	ended := make(chan struct{})
	var once sync.Once
	cc.SetStateHook(func(cc *http.ClientConn) {
		if cc.Err() != nil {
			once.Do(func() { close(ended) })
		}
	})
	g.connected(cc)
	defer g.disconnected(cc)
	<-ended
}

// preamble reads the backend's line and says what to answer, and whether
// HTTP/2 follows.
func (g *Gateway) preamble(c io.Reader, pub ed25519.PublicKey) (string, bool) {
	f, err := readLine(c)
	if err != nil || len(f) < 3 || f[0] != preambleTag {
		return "NO preamble", false
	}
	mode, version := f[1], f[2]
	g.mu.Lock()
	defer g.mu.Unlock()
	// Another version is refused before anything else, pairing included:
	// both sides must be the same release.
	if version != g.version {
		if g.status.Mismatch != version {
			g.logger.Warn("link: the backend's version isn't the gateway's", "backend", version, "gateway", g.version)
		}
		g.status.Mismatch, g.mismatchAt = version, time.Now()
		return "NO version " + g.version, false
	}
	switch mode {
	case "link":
		if g.backend == nil || !bytes.Equal(pub, g.backend) {
			g.logger.Warn("link: a backend that isn't this gateway's was refused", "key", Fingerprint(pub))
			return "NO not-paired", false
		}
	case "pair":
		if len(f) < 4 {
			return "NO preamble", false
		}
		theirs, err := hexProof(f[3])
		if err != nil {
			return "NO preamble", false
		}
		code, err := codeBytes(g.code())
		if err != nil {
			g.logger.Warn("link: a backend asked to pair, but this gateway has no pairing code (OVERFIN_PAIRING)")
			return "NO no-code", false
		}
		sum := sha256.Sum256(code)
		// Every code used is remembered, by its hash, one a line.
		used, _ := os.ReadFile(filepath.Join(g.dir, "used-code"))
		if slices.Contains(strings.Fields(string(used)), hex.EncodeToString(sum[:])) {
			g.logger.Warn("link: a backend asked to pair with a code already used; make a new one on the backend")
			return "NO code-used", false
		}
		if subtle.ConstantTimeCompare(theirs, proof(code, "backend", g.id.Public(), pub)) != 1 {
			g.failures++
			g.logger.Warn("link: a backend asked to pair with another code", "attempts", g.failures)
			return "NO wrong-code", false
		}
		if err := writeFile(filepath.Join(g.dir, "backend.pub"), []byte(hex.EncodeToString(pub)+"\n")); err != nil {
			g.logger.Error("link: the backend's key couldn't be saved", "err", err)
			return "NO error", false
		}
		if err := writeFile(filepath.Join(g.dir, "used-code"), append(used, hex.EncodeToString(sum[:])+"\n"...)); err != nil {
			g.logger.Error("link: the used code couldn't be saved", "err", err)
		}
		g.failures = 0
		g.backend = pub
		g.status.Paired, g.status.Backend = true, Fingerprint(pub)
		g.logger.Info("link: paired with a backend", "key", Fingerprint(pub))
		return "OK " + hex.EncodeToString(proof(code, "gateway", g.id.Public(), pub)), true
	default:
		return "NO preamble", false
	}
	return "OK", true
}

func (g *Gateway) connected(cc *http.ClientConn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cc != nil {
		g.cc.Close() // a backend that came back: its former connection is gone
	}
	g.cc = cc
	g.status.Connected, g.status.Mismatch, g.status.BackendVersion, g.status.Since = true, "", g.version, time.Now()
	g.logger.Info("link: the backend is connected")
}

func (g *Gateway) disconnected(cc *http.ClientConn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cc != cc {
		return
	}
	g.cc = nil
	g.status.Connected, g.status.Since = false, time.Now()
	g.logger.Warn("link: the backend's connection ended")
}

// RoundTrip sends a request to the backend over the link.
func (g *Gateway) RoundTrip(req *http.Request) (*http.Response, error) {
	g.mu.Lock()
	cc := g.cc
	g.mu.Unlock()
	if cc == nil {
		return nil, ErrNoBackend
	}
	r := req.Clone(req.Context())
	r.URL.Scheme, r.URL.Host = "http", "backend"
	r.Host = "backend"
	return cc.RoundTrip(r)
}

// Client sends requests to the backend over the link.
func (g *Gateway) Client() *http.Client { return &http.Client{Transport: g} }

// Wait waits until the backend is connected, or ctx ends.
func (g *Gateway) Wait(ctx context.Context) error {
	for !g.Status().Connected {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil
}

// Close ends the backend's connection (the gateway is stopping).
func (g *Gateway) Close() {
	g.mu.Lock()
	cc := g.cc
	g.mu.Unlock()
	if cc != nil {
		cc.Close()
	}
}
