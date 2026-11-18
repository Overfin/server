package msfp

import (
	"context"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/overfin/server/msfp/wire"
)

// Limits on the calls peers make here, and what this server's own calls
// remember of a peer that asked it to wait (MSFP core section 8.3). In
// memory only. Copied from Overfin's internal/federation/limits.go, with
// its numbers as CallLimits.

// forgetBucket: buckets unused for this long are forgotten.
const forgetBucket = 10 * time.Minute

// maxAddrBuckets caps the addresses whose limits are remembered at once.
const maxAddrBuckets = 100_000

// backoffFirst is how long this server's background calls hold off
// after the first call a peer didn't answer, doubling each time, up to
// an hour (wire.MaxRetryAfter), give or take a quarter.
const backoffFirst = 2 * time.Second

type callLimits struct {
	l CallLimits

	mu    sync.Mutex
	addrs map[netip.Addr]*bucket
	peers map[string]*bucket // by key
	plays map[string]*bucket // by key
	all   *bucket
	swept time.Time

	peerSlots, allSlots *slots

	// waits are this server's own calls' memory, by the peer's key.
	waits map[string]*wait
}

func newCallLimits(l CallLimits) *callLimits {
	return &callLimits{l: l,
		addrs: map[netip.Addr]*bucket{}, peers: map[string]*bucket{}, plays: map[string]*bucket{},
		peerSlots: &slots{size: l.PeerAtOnce, queue: 2 * l.PeerAtOnce, wait: l.QueueWait},
		allSlots:  &slots{size: l.AllAtOnce, queue: 2 * l.AllAtOnce, wait: l.QueueWait},
		waits:     map[string]*wait{},
	}
}

// bucket is a token bucket.
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
	return false, max(1, min(wire.MaxRetryAfter, int(math.Ceil((1-b.tokens)/rate))))
}

func bucketOf(m map[string]*bucket, k string, now time.Time, burst float64) *bucket {
	b := m[k]
	if b == nil {
		b = &bucket{tokens: burst, last: now}
		m[k] = b
	}
	return b
}

// sweep forgets buckets unused for a while; l.mu is held.
func (l *callLimits) sweep(now time.Time) {
	if l.all == nil {
		l.all = &bucket{tokens: l.l.AllBurst, last: now}
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
func (l *callLimits) fromAddr(addr netip.Addr, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)
	addr = limitAddr(addr)
	b := l.addrs[addr]
	if b == nil {
		if len(l.addrs) >= maxAddrBuckets {
			forgetFull(l.addrs, now, l.l.AddressBurst/l.l.AddressRate)
		}
		b = &bucket{tokens: l.l.AddressBurst, last: now}
		l.addrs[addr] = b
	}
	ok, _ := b.take(now, l.l.AddressRate, l.l.AddressBurst)
	return ok
}

// forgetFull makes room among buckets: those full again (unused for
// refill seconds), which a new bucket would be anyway; when none are,
// all of them, rather than the memory growing.
func forgetFull(m map[netip.Addr]*bucket, now time.Time, refill float64) {
	for k, b := range m {
		if now.Sub(b.last).Seconds() >= refill {
			delete(m, k)
		}
	}
	if len(m) >= maxAddrBuckets {
		clear(m)
	}
}

// limitAddr is the address limits count by: an IPv4 address, or an IPv6
// address's /64.
func limitAddr(a netip.Addr) netip.Addr {
	a = a.Unmap()
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.Addr()
	}
	return a
}

// refusal is a limit reached: the problem to answer, and when to try
// again.
type refusal struct {
	problem    problemType
	retryAfter int
}

// call counts a known peer's call (play or not) against its limits and
// all peers': nil when it may go on.
func (l *callLimits) call(key string, play bool, now time.Time) *refusal {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)
	// Each bucket is taken from only once all have a token: a call one of
	// them refuses costs the others nothing.
	peer := bucketOf(l.peers, key, now, l.l.PeerBurst)
	if ok, after := peer.take(now, l.l.PeerRate, l.l.PeerBurst); !ok {
		return &refusal{probRateLimited, after}
	}
	if play {
		if ok, after := bucketOf(l.plays, key, now, l.l.PlayBurst).take(now, l.l.PlayRate, l.l.PlayBurst); !ok {
			peer.tokens++
			return &refusal{probRateLimited, after}
		}
	}
	if ok, after := l.all.take(now, l.l.AllRate, l.l.AllBurst); !ok {
		peer.tokens++
		if play {
			l.plays[key].tokens++
		}
		return &refusal{probServerBusy, after}
	}
	return nil
}

// slots takes a request's slots, its peer's own (none for a stranger's)
// and all peers', waiting in their queues as long as they allow; it
// gives what frees them, or the refusal.
func (l *callLimits) slots(ctx context.Context, key string, known bool) (func(), *refusal) {
	freeOwn := func() {}
	if known {
		f, ok := l.peerSlots.take(ctx, key)
		if !ok {
			return nil, &refusal{probRateLimited, 1}
		}
		freeOwn = f
	}
	freeAll, ok := l.allSlots.take(ctx, "")
	if !ok {
		freeOwn()
		return nil, &refusal{probServerBusy, 1}
	}
	return func() { freeAll(); freeOwn() }, nil
}

// clientKey is the context key of WithClient.
type clientKey struct{}

// WithClient gives r the address it comes from, as the host's trusted
// proxies say: what the limits by network address count by (MSFP core
// section 8.3). Without it, the engine counts by the connection's
// address.
func WithClient(r *http.Request, addr netip.Addr) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), clientKey{}, addr))
}

// clientAddr is the address a request comes from.
func (e *Engine) clientAddr(r *http.Request) netip.Addr {
	if e.cfg.ClientAddress != nil {
		return e.cfg.ClientAddress(r).Unmap()
	}
	if a, ok := r.Context().Value(clientKey{}).(netip.Addr); ok && a.IsValid() {
		return a.Unmap()
	}
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr().Unmap()
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if a, err := netip.ParseAddr(host); err == nil {
			return a.Unmap()
		}
	}
	return netip.Addr{}
}

// The other side: this server's own calls to a peer.

// wait is what this server's calls remember of a peer: until when it
// asked them to wait, and, after calls it didn't answer, until when
// calls made in the background hold off.
type wait struct {
	retryAt   time.Time
	backoffAt time.Time
	failures  int
	last      time.Time
}

// mayCall says whether a call to the peer with key may go now: an
// operator's call goes unless the peer asked to wait (a signed
// retryAfter); a background one also holds off after calls the peer
// didn't answer.
func (l *callLimits) mayCall(key string, background bool, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)
	w := l.waits[key]
	return w == nil || (!now.Before(w.retryAt) && (!background || !now.Before(w.backoffAt)))
}

// heard keeps what a call to the peer with key gave: a signed answer
// (status, and its retryAfter for a 429 or 503), or none.
func (l *callLimits) heard(key string, answered bool, status, retryAfter int, now time.Time) {
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
// went unanswered.
func backoff(n int) time.Duration {
	d := backoffFirst << min(n-1, 20)
	d = min(d, wire.MaxRetryAfter*time.Second)
	return min(time.Duration(float64(d)*(0.75+rand.Float64()/2)), wire.MaxRetryAfter*time.Second)
}
