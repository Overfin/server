package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/overfin/server/internal/identity"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/slots"
)

// Limits on the federation calls other servers make here, and what this
// server's own calls remember of a peer that asked it to wait (MSFP core
// section 8.3). In memory only. Generous: a paired server's normal use
// (a catalog read a page of 5,000 titles at a time, a progress every 45
// seconds per playback, a hello every 20 seconds while a pairing waits)
// stays far below them; media fetches aren't federation calls, and have
// bounds of their own (playback's stream routes).
const (
	// Per network address, before any signature is checked: the same as
	// the gateway's first filter.
	addrRate, addrBurst = 50, 400
	// Per paired server (by its key), once its signature checked out.
	peerRate, peerBurst = 20, 200
	// All paired servers together.
	allRate, allBurst = 200, 2000
	// play, per paired server, besides the pairing's streams: each can
	// start work on the media. 30 a minute, 20 at once.
	playRate, playBurst = 0.5, 20

	// Requests handled at once, per paired server and in all, each with a
	// queue of its own, waited in at most queueWait.
	peerAtOnce, peerQueue = 8, 16
	allAtOnce, allQueue   = 64, 128
	queueWait             = 5 * time.Second

	// Buckets not used for this long are forgotten.
	forgetBucket = 10 * time.Minute

	// What this server's own calls do after no answer: wait from
	// backoffFirst, doubling each time, up to maxRetryAfter (MSFP's "an
	// hour at most"), give or take a quarter.
	backoffFirst  = 2 * time.Second
	maxRetryAfter = 3600
)

// ErrBackingOff is a call not sent: the peer asked to wait (a signed
// retryAfter) and the time isn't up, or, for a call made in the
// background, it didn't answer lately and the next try isn't due.
var ErrBackingOff = fmt.Errorf("%w: it asked to wait, or didn't answer lately", ErrUnreachable)

// Limits holds federation's limits. Its zero value is ready to use.
type Limits struct {
	// Now is the clock, time.Now unless a test sets it.
	Now func() time.Time

	mu    sync.Mutex
	addrs map[netip.Addr]*bucket
	peers map[string]*bucket // by key
	plays map[string]*bucket // by key
	all   *bucket
	swept time.Time

	peerSlots, allSlots *slots.Slots

	// waits are this server's own calls' memory, by the peer's key.
	waits map[string]*wait
}

type bucket struct {
	tokens float64
	last   time.Time
}

// take takes a token from b, refilled at rate up to burst; when there's
// none, how many seconds until there is one.
func (b *bucket) take(now time.Time, rate, burst float64) (ok bool, retryAfter int) {
	b.tokens = min(burst, b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, max(1, min(maxRetryAfter, int(math.Ceil((1-b.tokens)/rate))))
}

func (l *Limits) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Limits) bucketOf(m map[string]*bucket, k string, now time.Time, burst float64) *bucket {
	b := m[k]
	if b == nil {
		b = &bucket{tokens: burst, last: now}
		m[k] = b
	}
	return b
}

// sweep forgets buckets unused for a while; l.mu is held.
func (l *Limits) sweep(now time.Time) {
	if l.addrs == nil {
		l.addrs, l.peers, l.plays = map[netip.Addr]*bucket{}, map[string]*bucket{}, map[string]*bucket{}
		l.all = &bucket{tokens: allBurst, last: now}
		l.peerSlots = &slots.Slots{Size: peerAtOnce, Queue: peerQueue, Wait: queueWait}
		l.allSlots = &slots.Slots{Size: allAtOnce, Queue: allQueue, Wait: queueWait}
		l.waits = map[string]*wait{}
	}
	if now.Sub(l.swept) < forgetBucket {
		return
	}
	l.swept = now
	for k, b := range l.addrs {
		if now.Sub(b.last) > forgetBucket {
			delete(l.addrs, k)
		}
	}
	for _, m := range []map[string]*bucket{l.peers, l.plays} {
		for k, b := range m {
			if now.Sub(b.last) > forgetBucket {
				delete(m, k)
			}
		}
	}
	for k, w := range l.waits {
		if now.After(w.retryAt) && now.After(w.backoffAt) && now.Sub(w.last) > forgetBucket {
			delete(l.waits, k)
		}
	}
}

// fromAddr says whether a request from addr may go on to its signature
// check.
func (l *Limits) fromAddr(addr netip.Addr, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)
	b := l.addrs[addr]
	if b == nil {
		b = &bucket{tokens: addrBurst, last: now}
		l.addrs[addr] = b
	}
	ok, _ := b.take(now, addrRate, addrBurst)
	return ok
}

// refusal is a limit reached: the problem to answer, and when to try
// again.
type refusal struct {
	problem    problem.Type
	retryAfter int
}

// call counts a known peer's call (play or not) against its limits and
// all peers': nil when it may go on.
func (l *Limits) call(key string, play bool, now time.Time) *refusal {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)
	if ok, after := l.bucketOf(l.peers, key, now, peerBurst).take(now, peerRate, peerBurst); !ok {
		return &refusal{problem.RateLimited, after}
	}
	if play {
		if ok, after := l.bucketOf(l.plays, key, now, playBurst).take(now, playRate, playBurst); !ok {
			return &refusal{problem.RateLimited, after}
		}
	}
	if ok, after := l.all.take(now, allRate, allBurst); !ok {
		return &refusal{problem.ServerBusy, after}
	}
	return nil
}

// slots takes a known peer's request's slots, its own (or, for a
// stranger's, none) and all peers', waiting in their queues as long as
// they allow; it gives what frees them, or the refusal.
func (l *Limits) slots(ctx context.Context, key string, known bool) (func(), *refusal) {
	l.mu.Lock()
	l.sweep(l.now())
	own, all := l.peerSlots, l.allSlots
	l.mu.Unlock()
	freeOwn := func() {}
	if known {
		f, ok := own.Take(ctx, key)
		if !ok {
			return nil, &refusal{problem.RateLimited, 1}
		}
		freeOwn = f
	}
	freeAll, ok := all.Take(ctx, "")
	if !ok {
		freeOwn()
		return nil, &refusal{problem.ServerBusy, 1}
	}
	return func() { freeAll(); freeOwn() }, nil
}

// sendRefusal answers a limit reached: its problem, with retryAfter in the
// body (signed, when w is a route's and the caller known) and the header.
func sendRefusal(w http.ResponseWriter, r *refusal) {
	p := problem.New(r.problem, "")
	p.RetryAfter = r.retryAfter
	problem.Write(w, p)
}

// clientAddr is the address a request comes from, as identity found it
// (behind trusted proxies, the visitor's), else the connection's.
func clientAddr(r *http.Request) netip.Addr {
	if a := identity.ClientIP(r.Context()); a.IsValid() {
		return a.Unmap()
	}
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr().Unmap()
	}
	return netip.Addr{}
}

// The other side: this server's own calls to a peer.

// wait is what this server's calls remember of a peer: until when it
// asked them to wait, and, after calls it didn't answer, until when calls
// made in the background hold off.
type wait struct {
	retryAt   time.Time
	backoffAt time.Time
	failures  int
	last      time.Time
}

type backgroundKey struct{}

// Background marks ctx's calls as made by this server by itself, on a
// schedule (a progress, a catalog read, a hello while a pairing waits):
// after calls the peer didn't answer, they hold off, longer each time
// (MSFP core section 8.3). Calls someone asked for go when asked, unless
// the peer said to wait.
func Background(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundKey{}, true)
}

func background(ctx context.Context) bool {
	b, _ := ctx.Value(backgroundKey{}).(bool)
	return b
}

// mayCall says whether a call to the peer with key may go now.
func (l *Limits) mayCall(key string, background bool, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)
	w := l.waits[key]
	return w == nil || (!now.Before(w.retryAt) && (!background || !now.Before(w.backoffAt)))
}

// heard keeps what a call to the peer with key gave: a signed answer
// (status, and its retryAfter for a 429 or 503), or none (answered false).
func (l *Limits) heard(key string, answered bool, status, retryAfter int, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)
	w := l.waits[key]
	if w == nil {
		w = &wait{}
		l.waits[key] = w
	}
	w.last = now
	busy := status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable
	switch {
	case answered && busy && retryAfter > 0:
		w.retryAt = now.Add(time.Duration(retryAfter) * time.Second)
	case !answered || busy:
		w.failures++
		w.backoffAt = now.Add(backoff(w.failures))
	default:
		w.failures, w.backoffAt = 0, time.Time{}
	}
}

// backoff is how long background calls hold off after n calls in a row
// went unanswered: backoffFirst, doubling, at most maxRetryAfter seconds,
// give or take a quarter so that servers don't all try again together.
func backoff(n int) time.Duration {
	d := backoffFirst << min(n-1, 20)
	d = min(d, maxRetryAfter*time.Second)
	return min(time.Duration(float64(d)*(0.75+rand.Float64()/2)), maxRetryAfter*time.Second)
}

// retryAfterOf is a signed refusal's retryAfter, 0 when it has none in
// range (1 to 3,600 seconds).
func retryAfterOf(body []byte) int {
	var p struct {
		RetryAfter int `json:"retryAfter"` // a fraction or a string: not one
	}
	if json.Unmarshal(body, &p) != nil || p.RetryAfter < 1 || p.RetryAfter > maxRetryAfter {
		return 0
	}
	return p.RetryAfter
}

// waitTurn waits until a call to p may go again, when the peer asked for
// no more than limit; false when it asked longer, or ctx ended first.
func (s *Service) waitTurn(ctx context.Context, r Reply, limit time.Duration) bool {
	d := time.Duration(r.RetryAfter) * time.Second
	if r.RetryAfter <= 0 || d > limit {
		return false
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
