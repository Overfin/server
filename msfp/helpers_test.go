package msfp_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/overfin/server/msfp"
	"github.com/overfin/server/msfp/store/memory"
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
	see  func(*http.Request, []byte)
}

func (s *server) onRequest(see func(*http.Request, []byte)) {
	s.mu.Lock()
	s.see = see
	s.mu.Unlock()
}

// exchange is a request and its answer, as they went on the wire.
type exchange struct {
	reqBody, reqSig, resBody, resSig string
	status                           int
}

func (s *server) lastExchange() exchange {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// newServer starts a TLS test server, then an engine at its address,
// configured by cfg (Address, Store, Notifier and AllowLoopback are set
// here when left empty).
func newServer(t *testing.T, cfg msfp.Config) *server {
	t.Helper()
	s := &server{events: &events{}}
	var h http.Handler = http.NotFoundHandler()
	s.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		see := s.see
		s.mu.Unlock()
		if see != nil {
			see(r.Clone(r.Context()), body)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		s.mu.Lock()
		s.last = exchange{reqBody: string(body), reqSig: r.Header.Get("MSFP-Signature"),
			resBody: rec.Body.String(), resSig: rec.Header().Get("MSFP-Signature"), status: rec.Code}
		s.mu.Unlock()
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		w.Write(rec.Body.Bytes())
	}))
	t.Cleanup(s.srv.Close)
	if cfg.Address == "" {
		cfg.Address = s.srv.URL
	}
	if cfg.Store == nil {
		cfg.Store = memory.New()
	}
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
	s.eng = eng
	h = eng.FederationHandler()
	return s
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

// pair pairs b with a: a makes a code, b redeems it, a accepts.
func pair(t *testing.T, a, b *server) (aSide, bSide msfp.PeerView) {
	t.Helper()
	ctx := context.Background()
	code, _, err := a.eng.MakeCode(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.eng.RedeemCode(ctx, code); err != nil {
		t.Fatal(err)
	}
	waiting := onlyPeer(t, a.eng)
	if _, err := a.eng.Accept(ctx, waiting.ID); err != nil {
		t.Fatal(err)
	}
	return onlyPeer(t, a.eng), onlyPeer(t, b.eng)
}
