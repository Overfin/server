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

// maxStreams is how many requests the link carries at once, every
// visitor's together (the backend's limit, as HTTP/2's server): Go's own
// default, stated so a new Go can't change it. A request past it waits for
// a stream to end.
const maxStreams = 250

// h2Config is both sides' HTTP/2 settings: frames larger than the default
// 16 KiB (fewer of them for video), 4 MiB windows for each request, at most
// maxStreams requests at once, and a ping when the connection goes quiet,
// so a dead link is noticed. The connection's window is room for every
// request's at once (Go's own client default, 1 GiB): a window comes back
// only as its response is read, so with one stream's size a single
// response nobody reads (a paused viewer's film) held up every other.
// It's credit, not memory: what's held is at most a stream's window each.
func h2Config() *http.HTTP2Config {
	return &http.HTTP2Config{
		MaxConcurrentStreams:          maxStreams,
		MaxReadFrameSize:              1 << 20,
		MaxReceiveBufferPerConnection: 1 << 30,
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
