package link

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// After the preamble, the link's TLS connection carries HTTP/2 with the
// roles flipped. To net/http it's a plain connection already open: HTTP/2
// without TLS of its own (the TLS under it is the link's, checked above),
// "prior knowledge", no HTTP/1.

// h2Config is both sides' HTTP/2 settings: frames larger than the default
// 16 KiB (fewer of them for video), the largest windows net/http allows,
// and a ping when the connection goes quiet, so a dead link is noticed.
func h2Config() *http.HTTP2Config {
	return &http.HTTP2Config{
		MaxReadFrameSize:              1 << 20,
		MaxReceiveBufferPerConnection: 4<<20 - 1,
		MaxReceiveBufferPerStream:     4<<20 - 1,
		SendPingTimeout:               15 * time.Second,
		PingTimeout:                   10 * time.Second,
	}
}

func h2Only() *http.Protocols {
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	return &p
}

// plainConn hides the TLS connection's type from net/http, which would
// otherwise look at its ALPN name to choose a protocol.
type plainConn struct{ net.Conn }

// clientConn starts HTTP/2 on c as the client (the gateway).
func clientConn(ctx context.Context, c net.Conn) (*http.ClientConn, error) {
	var once sync.Once
	tr := &http.Transport{
		Protocols: h2Only(),
		HTTP2:     h2Config(),
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			var conn net.Conn
			once.Do(func() { conn = plainConn{c} })
			if conn == nil {
				return nil, errors.New("the link has one connection")
			}
			return conn, nil
		},
	}
	return tr.NewClientConn(ctx, "http", "backend:80")
}

// oneConn is a listener that hands out one connection, then waits until
// it's closed.
type oneConn struct {
	conn   net.Conn
	once   sync.Once
	closed chan struct{}
	served bool
	mu     sync.Mutex
}

func (l *oneConn) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.served {
		l.served = true
		l.mu.Unlock()
		return &notifyConn{Conn: l.conn, closed: l.close}, nil
	}
	l.mu.Unlock()
	<-l.closed
	return nil, net.ErrClosed
}

func (l *oneConn) close()         { l.once.Do(func() { close(l.closed) }) }
func (l *oneConn) Close() error   { l.close(); return nil }
func (l *oneConn) Addr() net.Addr { return l.conn.LocalAddr() }

// notifyConn says when net/http closes the connection.
type notifyConn struct {
	net.Conn
	closed func()
}

func (c *notifyConn) Close() error { c.closed(); return c.Conn.Close() }

// serveConn serves HTTP/2 on c as the server (the backend) with handler,
// until the connection ends or ctx does.
func serveConn(ctx context.Context, c net.Conn, handler http.Handler) {
	ln := &oneConn{conn: plainConn{c}, closed: make(chan struct{})}
	srv := &http.Server{Handler: handler, Protocols: h2Only(), HTTP2: h2Config(),
		BaseContext: func(net.Listener) context.Context { return ctx }}
	go srv.Serve(ln)
	select {
	case <-ln.closed:
	case <-ctx.Done():
	}
	srv.Close()
	c.Close()
}
