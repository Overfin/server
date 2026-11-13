package gateway

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
)

// The visitor's address, as the gateway passes it to the backend: the
// connection's (the PROXY protocol's, from a trusted proxy in front: see
// proxyproto.go), or, from a trusted HTTP proxy in front (Caddy, say), the
// last address its X-Forwarded-For names that isn't one of them.

type addrKey struct{}

// HTTPProxies are reverse proxies in front of the gateway whose
// X-Forwarded-For is trusted (OVERFIN_TRUSTED_PROXIES); none by default.
var HTTPProxies []netip.Prefix

// ClientAddr is the visitor's host:port.
func ClientAddr(r *http.Request) string {
	if v, ok := r.Context().Value(addrKey{}).(string); ok {
		return v
	}
	return r.RemoteAddr
}

func trusted(a netip.Addr) bool {
	for _, p := range HTTPProxies {
		if p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

func withClientAddr(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addr := r.RemoteAddr
		if peer, err := netip.ParseAddrPort(r.RemoteAddr); err == nil && trusted(peer.Addr()) {
			hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
			for i := len(hops) - 1; i >= 0; i-- {
				a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
				if err != nil {
					break
				}
				if !trusted(a) {
					addr = netip.AddrPortFrom(a.Unmap(), 0).String()
					break
				}
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), addrKey{}, addr)))
	})
}

// ParsePrefixes reads a list of addresses and ranges (comma or space
// separated).
func ParsePrefixes(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if p, err := netip.ParsePrefix(f); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(f)
		if err != nil {
			return nil, err
		}
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}
