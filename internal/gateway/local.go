package gateway

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/overfin/server/internal/link"
)

// The bundled gateway: the one beside its backend, on the same machine
// (OVERFIN_LOCAL_SOCKET). It reaches the backend through a unix socket in
// a volume only the two share, not through the link: no TLS, no pairing,
// no events, no cache. Anything that can open the socket is the gateway,
// so the volume is shared with nothing else. Every backend has one: its
// LAN keeps the site when the internet is down. A remote gateway, if any,
// uses the link as before.

// Socket is the backend beside the gateway, through its socket.
type Socket struct {
	version string
	tr      *http.Transport

	mu     sync.Mutex
	status link.Status
}

// NewSocket reaches the backend through the socket at path; version is
// the gateway's own, which the backend's must be.
func NewSocket(path, version string) *Socket {
	d := &net.Dialer{Timeout: 5 * time.Second}
	return &Socket{
		version: version,
		tr: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return d.DialContext(ctx, "unix", path)
			},
			MaxIdleConnsPerHost: 64,
			IdleConnTimeout:     90 * time.Second,
			// Streams pass as they come, never re-encoded on the way.
			DisableCompression: true,
		},
		status: link.Status{Paired: true, Since: time.Now()},
	}
}

// RoundTrip sends a request to the backend through the socket. A backend
// that isn't there, or runs another version, is link.ErrNoBackend: the
// visitor gets the gateway's own page.
func (s *Socket) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.URL.Scheme, r.URL.Host = "http", "backend"
	if r.Host == "" {
		r.Host = "backend"
	}
	r.Header.Set(link.LocalVersion, s.version)
	resp, err := s.tr.RoundTrip(r)
	if err != nil {
		if req.Context().Err() == nil {
			s.set(false, "")
		}
		return nil, fmt.Errorf("%w: %v", link.ErrNoBackend, err)
	}
	if v := resp.Header.Get(link.LocalVersion); resp.StatusCode == http.StatusServiceUnavailable && v != "" && v != s.version {
		resp.Body.Close()
		s.set(true, v)
		return nil, fmt.Errorf("%w: it runs version %s", link.ErrNoBackend, v)
	}
	s.set(true, "")
	return resp, nil
}

func (s *Socket) set(connected bool, mismatch string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.Connected != connected {
		s.status.Since = time.Now()
	}
	s.status.Connected, s.status.Mismatch = connected, mismatch
	if connected && mismatch == "" {
		s.status.BackendVersion = s.version
	}
}

// Status is what the gateway last saw of its backend.
func (s *Socket) Status() link.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// BackendKey is nil: beside its backend, the gateway checks no session
// itself (the backend does).
func (s *Socket) BackendKey() ed25519.PublicKey { return nil }

// PrivateOnly answers only visitors on private addresses (the LAN, a VPN),
// straight on the connection: the bundled gateway's plain-http site is
// never the internet's. Others get the same 404 as any refused path.
func PrivateOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, err := netip.ParseAddrPort(r.RemoteAddr)
		if err != nil || !privateAddr(peer.Addr()) {
			notFound(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// privateAddr is config.PrivateAddr, which the gateway doesn't import:
// loopback, link-local, the private IPv4 ranges, the shared range VPNs use
// (100.64.0.0/10), and unique-local IPv6.
func privateAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || cgnat.Contains(ip)
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")
