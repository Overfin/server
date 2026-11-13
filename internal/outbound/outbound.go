// Package outbound is how Overfin reaches other servers: its paired ones
// (federation's calls, their streams). By default, straight from where it
// runs. On the backend, through its gateway (internal/link, "egress"): the
// backend makes no connection to the internet for federation, and peers
// only ever see the gateway.
package outbound

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"
)

var (
	mu   sync.RWMutex
	dial func(ctx context.Context, network, addr string) (net.Conn, error) = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
)

// Through has every connection to other servers made by d from now on (the
// backend: its gateway's egress).
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
