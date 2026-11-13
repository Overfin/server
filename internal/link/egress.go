package link

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Egress: the backend's way out to its paired servers, through its
// gateway. The backend opens a second connection to the gateway, with
// another ALPN name, the same pinned keys and version; its first line names
// where it wants to go (host:port). The gateway asks the backend, over the
// link, whether that's an address it means to reach, connects there, and
// pipes the bytes both ways. TLS stays end to end between the backend and
// the other server: the gateway sees only how much passes, and where.

// EgressALPN is the egress connection's TLS protocol name.
const EgressALPN = "overfin-egress"

// egressAsked is how long an address the backend dialed stays one the
// gateway may connect to for it.
const egressAsked = time.Minute

// egressPath is where the gateway asks the backend, over the link, whether
// an address is one it means to reach.
const egressPath = "/link/v1/egress"

// DialEgress connects to addr (host:port) through the gateway: a
// connection the caller then speaks TLS over, as if dialed directly.
func (b *Backend) DialEgress(ctx context.Context, network, addr string) (net.Conn, error) {
	return b.dialEgress(ctx, network, addr, true)
}

// dialEgress dials; ask false skips noting the address (a test of the
// gateway's refusal).
func (b *Backend) dialEgress(ctx context.Context, network, addr string, ask bool) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("egress: %s isn't TCP", network)
	}
	p, err := b.load()
	if err != nil {
		return nil, err
	}
	pinned, err := hex.DecodeString(p.Gateway)
	if err != nil || len(pinned) != ed25519.PublicKeySize {
		return nil, errors.New("egress: not paired with a gateway")
	}
	if ask {
		b.asked(addr)
	}
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}
	raw, err := d.DialContext(ctx, "tcp", p.Address)
	if err != nil {
		return nil, err
	}
	c := tls.Client(raw, &tls.Config{
		Certificates:       []tls.Certificate{b.id.cert},
		NextProtos:         []string{EgressALPN},
		ServerName:         p.ServerName,
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // the gateway's key is checked below, against the pinned one
	})
	fail := func(err error) (net.Conn, error) { c.Close(); return nil, err }
	c.SetDeadline(time.Now().Add(handshakeWait))
	if err := c.HandshakeContext(ctx); err != nil {
		return fail(fmt.Errorf("egress: TLS with the gateway: %w", err))
	}
	cs := c.ConnectionState()
	gw, err := peerKey(cs)
	if err != nil || cs.NegotiatedProtocol != EgressALPN || !bytes.Equal(gw, pinned) {
		return fail(errors.New("egress: that isn't the paired gateway"))
	}
	if _, err := fmt.Fprintf(c, "%s egress %s %s\n", preambleTag, b.version, addr); err != nil {
		return fail(err)
	}
	f, err := readLine(c)
	if err != nil {
		return fail(fmt.Errorf("egress: the gateway's answer: %w", err))
	}
	if len(f) == 0 || f[0] != "OK" {
		return fail(fmt.Errorf("egress: the gateway refused %s: %v", addr, f))
	}
	c.SetDeadline(time.Time{})
	return c, nil
}

// asked notes that the backend means to reach addr.
func (b *Backend) asked(addr string) {
	b.egressMu.Lock()
	defer b.egressMu.Unlock()
	if b.egress == nil {
		b.egress = map[string]time.Time{}
	}
	now := time.Now()
	for k, t := range b.egress {
		if now.Sub(t) > egressAsked {
			delete(b.egress, k)
		}
	}
	b.egress[addr] = now
}

// meant says whether the backend asked for addr lately (the gateway's
// question, over the link).
func (b *Backend) meant(addr string) bool {
	b.egressMu.Lock()
	defer b.egressMu.Unlock()
	t, ok := b.egress[addr]
	return ok && time.Since(t) <= egressAsked
}

// linkRoutes are the backend's answers to its gateway's own questions,
// before its site: the gateway passes no visitor's request under /link/.
func (b *Backend) linkRoutes(site http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+egressPath, func(w http.ResponseWriter, r *http.Request) {
		if b.meant(r.URL.Query().Get("addr")) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	})
	mux.Handle("/", site)
	return mux
}

// serveEgress runs one egress connection on the gateway's side, once its
// TLS handshake is done.
func (g *Gateway) serveEgress(c *tls.Conn, pub ed25519.PublicKey) {
	f, err := readLine(c)
	refuse := func(reason string) { io.WriteString(c, "NO "+reason+"\n") }
	if err != nil || len(f) < 4 || f[0] != preambleTag || f[1] != "egress" {
		refuse("preamble")
		return
	}
	version, addr := f[2], f[3]
	g.mu.Lock()
	paired := g.backend != nil && bytes.Equal(pub, g.backend)
	g.mu.Unlock()
	switch {
	case !paired:
		refuse("not-paired")
		return
	case version != g.version:
		refuse("version")
		return
	}
	if _, _, err := net.SplitHostPort(addr); err != nil || !g.allowEgress(addr) {
		g.logger.Warn("link: an egress the backend didn't ask for was refused")
		refuse("not-allowed")
		return
	}
	out, err := (&net.Dialer{Timeout: 10 * time.Second}).Dial("tcp", addr)
	if err != nil {
		refuse("unreachable")
		return
	}
	defer out.Close()
	if _, err := io.WriteString(c, "OK\n"); err != nil {
		return
	}
	c.SetDeadline(time.Time{})
	var wg sync.WaitGroup
	wg.Add(2)
	var sent, received int64
	go func() {
		defer wg.Done()
		sent, _ = io.Copy(out, c)
		if tc, ok := out.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		received, _ = io.Copy(c, out)
		c.CloseWrite()
	}()
	wg.Wait()
	g.countEgress(addr, sent, received)
}

// allowEgress asks the backend, over the link, whether it means to reach
// addr.
func (g *Gateway) allowEgress(addr string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://backend"+egressPath+"?addr="+url.QueryEscape(addr), nil)
	resp, err := g.RoundTrip(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusNoContent
}

// Usage is the traffic of the egress to one address, since the gateway
// started: bytes sent there, bytes received.
type Usage struct{ Sent, Received int64 }

func (g *Gateway) countEgress(addr string, sent, received int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.usage == nil {
		g.usage = map[string]Usage{}
	}
	u := g.usage[addr]
	u.Sent += sent
	u.Received += received
	g.usage[addr] = u
}

// EgressUsage is the egress traffic by address, since the gateway started.
func (g *Gateway) EgressUsage() map[string]Usage {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]Usage, len(g.usage))
	for k, v := range g.usage {
		out[k] = v
	}
	return out
}
