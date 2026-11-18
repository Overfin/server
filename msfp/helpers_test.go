package msfp_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/overfin/server/msfp"
	"github.com/overfin/server/msfp/store/memory"
	"github.com/overfin/server/msfp/wire"
)

// fakeClock is a clock a test moves by hand.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(unix int64) *fakeClock { return &fakeClock{t: time.Unix(unix, 0).UTC()} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Set(unix int64) {
	c.mu.Lock()
	c.t = time.Unix(unix, 0).UTC()
	c.mu.Unlock()
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// secondsClock is the system's time cut to the second: an engine
// started at it hears requests dated the second it started (MSFP core
// section 6.4, step 4), which tests make at once.
type secondsClock struct{}

func (secondsClock) Now() time.Time { return time.Now().Truncate(time.Second) }

// script is randomness that gives, to a read of the length of the next
// chunk queued, that chunk; to any other read, real random bytes. So a
// test fixes the keys, secrets and nonces the vectors use, in the order
// the engine draws them, and leaves IDs random.
type script struct {
	mu     sync.Mutex
	chunks [][]byte
}

func (s *script) queue(hexes ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range hexes {
		b, err := hex.DecodeString(h)
		if err != nil {
			panic(err)
		}
		s.chunks = append(s.chunks, b)
	}
}

func (s *script) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.chunks) > 0 && len(s.chunks[0]) == len(p) {
		n := copy(p, s.chunks[0])
		s.chunks = s.chunks[1:]
		return n, nil
	}
	return rand.Read(p)
}

// events collects an engine's events.
type events struct {
	mu  sync.Mutex
	got []msfp.Event
}

func (e *events) Notify(ev msfp.Event) {
	e.mu.Lock()
	e.got = append(e.got, ev)
	e.mu.Unlock()
}

func (e *events) all() []msfp.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]msfp.Event(nil), e.got...)
}

// routeDialer sends every connection to one address, whatever the
// address asked: a vector's server at a.example.org is a test server
// here.
type routeDialer struct {
	mu sync.Mutex
	to string
}

func (d *routeDialer) set(to string) { d.mu.Lock(); d.to = to; d.mu.Unlock() }

func (d *routeDialer) Dial(ctx context.Context, network, _ string) (net.Conn, error) {
	d.mu.Lock()
	to := d.to
	d.mu.Unlock()
	if to == "" {
		return nil, errors.New("no route")
	}
	return (&net.Dialer{}).DialContext(ctx, network, to)
}

// server is an engine behind an httptest TLS server.
type server struct {
	eng    *msfp.Engine
	srv    *httptest.Server
	events *events
	// down makes the server answer every request with a plain 503, as if
	// it couldn't be reached.
	mu   sync.Mutex
	down bool
	// last is the last request served and its answer, as they went;
	// see, when set, is shown each request first.
	last exchange
	log  []exchange
	see  func(*http.Request, []byte)
	// h is the engine's handler (reopen swaps it); store its store.
	h     http.Handler
	store msfp.Store
	// rewrite, when set, changes the answers it says (ok) before they go.
	rewrite func(path string, h http.Header, body []byte) ([]byte, bool)
}

// setRewrite makes s a hostile peer: rewrite changes its answers.
func (s *server) setRewrite(rewrite func(path string, h http.Header, body []byte) ([]byte, bool)) {
	s.mu.Lock()
	s.rewrite = rewrite
	s.mu.Unlock()
}

// key is s's current key, read from its store (the memory store seals
// nothing).
func (s *server) key(t *testing.T) wire.Key {
	var seed []byte
	s.store.Read(context.Background(), func(tx msfp.Tx) error {
		r, err := tx.Server()
		if r != nil {
			seed = r.Current
		}
		return err
	})
	seed, err := msfp.PlainSealer{}.Open(seed, "msfp current key")
	if err != nil {
		t.Error(err)
	}
	k, err := wire.KeyFromSeed(seed)
	if err != nil {
		t.Error(err)
	}
	return k
}

func (s *server) onRequest(see func(*http.Request, []byte)) {
	s.mu.Lock()
	s.see = see
	s.mu.Unlock()
}

// exchange is a request and its answer, as they went on the wire.
type exchange struct {
	path, reqBody, reqSig, resBody, resSig string
	status                                 int
}

// exchanges are the requests served whose path ends with call, in order.
func (s *server) exchanges(call string) []exchange {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []exchange
	for _, x := range s.log {
		if strings.HasSuffix(x.path, "/"+call) {
			out = append(out, x)
		}
	}
	return out
}

func (s *server) lastExchange() exchange {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// handlerOf is an engine's handlers, as a host mounts them: the
// discovery document at /.well-known/msfp, the federation calls under
// the prefix.
func handlerOf(e *msfp.Engine) http.Handler {
	fed, disc, stream := e.FederationHandler(), e.DiscoveryHandler(), e.StreamHandler(nil)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/.well-known/msfp":
			disc.ServeHTTP(w, r)
		case strings.HasPrefix(r.URL.Path, "/api/v1/stream/"):
			stream.ServeHTTP(w, r)
		default:
			fed.ServeHTTP(w, r)
		}
	})
}

// serverOpts are what newServerWith adds to newServer: readPins leaves
// Config.Pins nil, so the engine reads them; cert is the certificate it
// serves (httptest's by default).
type serverOpts struct {
	readPins bool
	cert     *tls.Certificate
	// conn, when set, is told of every connection the server accepts.
	conn func()
}

// newServer starts a TLS test server, then an engine at its address,
// configured by cfg (Address, Store, Notifier and AllowLoopback are set
// here when left empty).
func newServer(t *testing.T, cfg msfp.Config) *server {
	t.Helper()
	return newServerWith(t, cfg, serverOpts{})
}

func newServerWith(t *testing.T, cfg msfp.Config, opts serverOpts) *server {
	t.Helper()
	s := &server{events: &events{}, h: http.NotFoundHandler()}
	s.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		down := s.down
		s.mu.Unlock()
		if down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		s.mu.Lock()
		see, h := s.see, s.h
		s.mu.Unlock()
		if see != nil {
			see(r.Clone(r.Context()), body)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		s.mu.Lock()
		rewrite := s.rewrite
		s.mu.Unlock()
		if rewrite != nil {
			// A hostile peer: its answer changed, and signed again with its
			// key when it was signed.
			length := rec.Header().Get("Content-Length")
			rec.Header().Del("Content-Length")
			if b, ok := rewrite(r.URL.Path, rec.Header(), rec.Body.Bytes()); !ok {
				rec.Header().Set("Content-Length", length)
			} else {
				rec.Body = bytes.NewBuffer(b)
				if rec.Header().Get(wire.HeaderSignature) != "" {
					rec.Header().Set(wire.HeaderSignature, wire.SignResponse(s.key(t), r.Header.Get(wire.HeaderNonce),
						rec.Code, rec.Header().Get("Content-Type"), b))
				}
			}
		}
		s.mu.Lock()
		s.last = exchange{path: r.URL.Path, reqBody: string(body), reqSig: r.Header.Get("MSFP-Signature"),
			resBody: rec.Body.String(), resSig: rec.Header().Get("MSFP-Signature"), status: rec.Code}
		s.log = append(s.log, s.last)
		s.mu.Unlock()
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		w.Write(rec.Body.Bytes())
	}))
	if opts.cert != nil {
		s.srv.TLS = &tls.Config{Certificates: []tls.Certificate{*opts.cert}}
	}
	if opts.conn != nil {
		s.srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
			if st == http.StateNew {
				opts.conn()
			}
		}
	}
	s.srv.StartTLS()
	t.Cleanup(s.srv.Close)
	if cfg.Address == "" {
		cfg.Address = s.srv.URL
	}
	if cfg.Pins == nil && !opts.readPins {
		// Its certificate's pin, named in its hellos, as a host with a
		// certificate of its own does (MSFP core section 3.2).
		cfg.Pins = []string{wire.Pin(s.srv.Certificate())}
	}
	if cfg.Store == nil {
		cfg.Store = memory.New()
	}
	s.store = cfg.Store
	if cfg.Notifier == nil {
		cfg.Notifier = s.events
	}
	if cfg.Dialer == nil {
		cfg.AllowLoopback = true
	}
	if cfg.Clock == nil {
		cfg.Clock = secondsClock{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	eng, err := msfp.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.eng, s.h = eng, handlerOf(eng)
	s.mu.Unlock()
	return s
}

// reopen starts the engine again at the same address, as after a
// restart, on cfg (Address, Notifier and Logger as newServer sets them).
func (s *server) reopen(t *testing.T, cfg msfp.Config) {
	t.Helper()
	cfg.Address = s.srv.URL
	if cfg.Pins == nil {
		cfg.Pins = []string{wire.Pin(s.srv.Certificate())}
	}
	if cfg.Notifier == nil {
		cfg.Notifier = s.events
	}
	if cfg.Dialer == nil {
		cfg.AllowLoopback = true
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	eng, err := msfp.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.eng, s.h, s.store = eng, handlerOf(eng), cfg.Store
	s.mu.Unlock()
}

func (s *server) setDown(down bool) {
	s.mu.Lock()
	s.down = down
	s.mu.Unlock()
}

// onlyPeer is the one peer an engine has.
func onlyPeer(t *testing.T, e *msfp.Engine) msfp.PeerView {
	t.Helper()
	peers, err := e.Peers(context.Background())
	if err != nil || len(peers) != 1 {
		t.Fatalf("peers: %v, %v", peers, err)
	}
	return peers[0]
}

// pair pairs b with a: a makes a code, b redeems it, a accepts. Each
// may have other peers.
func pair(t *testing.T, a, b *server) (aSide, bSide msfp.PeerView) {
	t.Helper()
	ctx := context.Background()
	code, _, err := a.eng.MakeCode(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	bSide, err = b.eng.RedeemCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	peers, _ := a.eng.Peers(ctx)
	for _, p := range peers {
		if p.State == msfp.Ours {
			aSide, err = a.eng.Accept(ctx, p.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if aSide.ID == "" {
		t.Fatal("no peer waiting on", a.srv.URL)
	}
	bSide, _ = b.eng.Peer(ctx, bSide.ID)
	return aSide, bSide
}

// mapDialer connects to the address mapped for the one asked, or to the
// one asked: so a test gives one server's address another destination
// for one engine alone. It checks nothing (a test's own Dialer).
type mapDialer struct {
	mu sync.Mutex
	to map[string]string
}

func (d *mapDialer) set(from, to string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.to == nil {
		d.to = map[string]string{}
	}
	d.to[from] = to
}

func (d *mapDialer) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	d.mu.Lock()
	to, ok := d.to[addr]
	d.mu.Unlock()
	if !ok {
		to = addr
	}
	return (&net.Dialer{}).DialContext(ctx, network, to)
}

// hostOf is a test server's host:port.
func hostOf(srv *httptest.Server) string { return srv.Listener.Addr().String() }

// alias is another listener for s's engine, as a new address of it: it
// answers as s does while on, with a plain 404 otherwise, and logs what
// it is asked. cert is its certificate (httptest's for nil).
type alias struct {
	srv *httptest.Server
	mu  sync.Mutex
	on  bool
	got []string // the paths asked
}

func (s *server) alias(t *testing.T, on bool, cert *tls.Certificate) *alias {
	t.Helper()
	a := &alias{on: on}
	a.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		on := a.on
		a.got = append(a.got, r.URL.Path)
		a.mu.Unlock()
		s.mu.Lock()
		h := s.h
		s.mu.Unlock()
		if !on {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	}))
	if cert != nil {
		a.srv.TLS = &tls.Config{Certificates: []tls.Certificate{*cert}}
	}
	a.srv.StartTLS()
	t.Cleanup(a.srv.Close)
	return a
}

func (a *alias) set(on bool) { a.mu.Lock(); a.on = on; a.mu.Unlock() }

func (a *alias) asked() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.got...)
}

// has says whether an event of type T was told, and gives the last.
func has[T msfp.Event](e *events) (T, bool) {
	var last T
	found := false
	for _, ev := range e.all() {
		if v, ok := ev.(T); ok {
			last, found = v, true
		}
	}
	return last, found
}

// waitAll waits for the background work of every engine given.
func waitAll(servers ...*server) {
	for _, s := range servers {
		s.eng.WaitForTest()
	}
}

// closedAddr is an address nothing listens at.
func closedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// selfSigned is a certificate for 127.0.0.1, its own.
func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: time.Unix(1700000000, 0), NotAfter: time.Unix(1900000000, 0)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv, Leaf: leaf}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// typeName is an event's type, as %T writes it.
func typeName(ev msfp.Event) string { return fmt.Sprintf("%T", ev) }
