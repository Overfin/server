package msfp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

// checkedDialer is the engine's default Dialer (MSFP core section 3.3):
// every address it connects to comes from a peer, so it refuses those
// that point back into this machine or its cloud. A name is looked up
// once, every address it gives is checked, and the address dialed is one
// checked, never looked up again (no DNS rebinding). Copied from
// Overfin's internal/outbound.
type checkedDialer struct {
	allowLoopback bool
	// lookup and connect are variables for tests.
	lookup  func(ctx context.Context, network, host string) ([]netip.Addr, error)
	connect func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Dial connects to addr (host:port), its addresses checked, each tried
// in turn.
func (d *checkedDialer) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	targets, err := d.resolve(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	connect := d.connect
	if connect == nil {
		connect = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
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

// resolve looks addr's host up once and gives the addresses to dial, all
// checked; ErrRefusedDestination when any address the name has is
// refused, so that a name can't mix a LAN address and the metadata
// service.
func (d *checkedDialer) resolve(ctx context.Context, network, addr string) ([]string, error) {
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
		return nil, fmt.Errorf("msfp: %s isn't TCP", network)
	}
	lookup := d.lookup
	if lookup == nil {
		lookup = net.DefaultResolver.LookupNetIP
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else if ips, err = lookup(ctx, family, host); err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("msfp: %s has no address", host)
	}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		if refusedDestination(ip, d.allowLoopback) {
			return nil, ErrRefusedDestination.with(fmt.Errorf("%s", host))
		}
		out = append(out, net.JoinHostPort(ip.Unmap().WithZone("").String(), port))
	}
	return out, nil
}

// refusedDestination says whether ip is one never dialed for a peer:
// loopback (unless allowed), link-local (a cloud's metadata service
// among them), the clouds' metadata services outside it, unspecified,
// multicast, and IPv4's "this network", reserved block and broadcast. An
// IPv4-mapped IPv6 address is judged as the IPv4 one, and so is the IPv4
// address inside a NAT64 (64:ff9b::/96) or 6to4 (2002::/16) one;
// local-use NAT64 (64:ff9b:1::/48) is refused whole.
func refusedDestination(ip netip.Addr, allowLoopback bool) bool {
	ip = ip.Unmap()
	if v4, ok := embeddedV4(ip); ok && refusedDestination(v4, allowLoopback) {
		return true
	}
	if ip.IsLoopback() {
		return !allowLoopback
	}
	return !ip.IsValid() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast() ||
		thisNetwork.Contains(ip) || reservedV4.Contains(ip) || metadata[ip] || nat64Local.Contains(ip)
}

var (
	thisNetwork = netip.MustParsePrefix("0.0.0.0/8")
	reservedV4  = netip.MustParsePrefix("240.0.0.0/4") // with 255.255.255.255
	nat64       = netip.MustParsePrefix("64:ff9b::/96")
	nat64Local  = netip.MustParsePrefix("64:ff9b:1::/48")
	sixToFour   = netip.MustParsePrefix("2002::/16")
	// Clouds' metadata services outside link-local: Alibaba's (in
	// 100.64.0.0/10), Azure's (a public address), AWS's over IPv6.
	metadata = map[netip.Addr]bool{
		netip.MustParseAddr("100.100.100.200"): true,
		netip.MustParseAddr("168.63.129.16"):   true,
		netip.MustParseAddr("fd00:ec2::254"):   true,
	}
)

// embeddedV4 is the IPv4 address a NAT64 or 6to4 address reaches.
func embeddedV4(ip netip.Addr) (netip.Addr, bool) {
	if !ip.Is6() {
		return netip.Addr{}, false
	}
	b := ip.As16()
	switch {
	case nat64.Contains(ip):
		return netip.AddrFrom4([4]byte(b[12:16])), true
	case sixToFour.Contains(ip):
		return netip.AddrFrom4([4]byte(b[2:6])), true
	}
	return netip.Addr{}, false
}
