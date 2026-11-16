// Package outbound is how Overfin reaches other servers: its paired ones
// (federation's calls, their streams). By default, straight from where it
// runs. On the backend, through its gateway (internal/link, "egress"): the
// backend makes no connection to the internet for federation, and peers
// only ever see the gateway.
//
// Either way, the addresses come from other servers (a pairing code, a
// pair request, an address change, a stream's URL), so they're checked
// before any connection (SECURITY.md rule 129): Overfin never dials its own
// machine, link-local addresses (a cloud's metadata service among them),
// unspecified, multicast or reserved ones for a peer. Private ranges stay
// allowed: paired servers may be on a LAN. A name is looked up once, every
// address it gives is checked, and the address dialed is one checked,
// never looked up again (no DNS rebinding).
package outbound

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

var (
	mu   sync.RWMutex
	dial func(ctx context.Context, network, addr string) (net.Conn, error) = Direct
)

// Through has every connection to other servers made by d from now on (the
// backend: its gateway's egress, which checks addresses the same way).
func Through(d func(ctx context.Context, network, addr string) (net.Conn, error)) {
	mu.Lock()
	dial = d
	mu.Unlock()
}

// DialContext connects to addr (host:port) the way Through set.
func DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	mu.RLock()
	d := dial
	mu.RUnlock()
	return d(ctx, network, addr)
}

// Transport is an HTTP transport whose connections go through DialContext:
// no proxy from the environment, TLS end to end with the other server.
func Transport() *http.Transport {
	return &http.Transport{
		DialContext:         DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
	}
}

// loopback is the operator's choice to reach paired servers on this
// machine ([federation] allow_loopback): off by default.
var loopback atomic.Bool

// AllowLoopback lets Overfin dial loopback addresses for peers (a paired
// server on the same machine), or not, the default. Nothing else refused
// is ever allowed.
func AllowLoopback(on bool) { loopback.Store(on) }

// ErrRefused is the error for an address Overfin never dials for a peer.
var ErrRefused = errors.New("an address Overfin doesn't connect to for a paired server")

// Refused says whether ip is one Overfin never dials for a peer:
// loopback (unless allowed), link-local (a cloud's metadata service among
// them), unspecified, multicast, and IPv4's "this network" (0.0.0.0/8),
// reserved block (240.0.0.0/4) and broadcast in it. An IPv4-mapped IPv6
// address is judged as the IPv4 one.
func Refused(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() {
		return !loopback.Load()
	}
	return !ip.IsValid() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast() ||
		thisNetwork.Contains(ip) || reservedV4.Contains(ip)
}

var (
	thisNetwork = netip.MustParsePrefix("0.0.0.0/8")
	reservedV4  = netip.MustParsePrefix("240.0.0.0/4") // with 255.255.255.255
)

// lookup finds a name's addresses (a variable: tests give their own).
var lookup = net.DefaultResolver.LookupNetIP

// Resolve looks addr's host up once and gives the addresses to dial
// (host:port, IP literals), all checked; ErrRefused when any address the
// name has is refused, so that a name can't mix a LAN address and the
// metadata service. network is "tcp", "tcp4" or "tcp6".
func Resolve(ctx context.Context, network, addr string) ([]string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	family := "ip"
	switch network {
	case "tcp":
	case "tcp4":
		family = "ip4"
	case "tcp6":
		family = "ip6"
	default:
		return nil, fmt.Errorf("outbound: %s isn't TCP", network)
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else if ips, err = lookup(ctx, family, host); err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("outbound: %s has no address", host)
	}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		if Refused(ip) {
			return nil, fmt.Errorf("%w: %s", ErrRefused, host)
		}
		out = append(out, net.JoinHostPort(ip.Unmap().WithZone("").String(), port))
	}
	return out, nil
}

// connect dials one checked address (a variable: tests see what's dialed).
var connect = (&net.Dialer{Timeout: 10 * time.Second}).DialContext

// Direct connects to addr from this machine: its addresses checked
// (Resolve), then each tried in turn.
func Direct(ctx context.Context, network, addr string) (net.Conn, error) {
	targets, err := Resolve(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, t := range targets {
		c, err := connect(ctx, network, t)
		if err == nil {
			return c, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.Join(errs...)
}
