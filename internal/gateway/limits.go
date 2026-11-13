package gateway

import (
	"net/http"
	"net/netip"
	"sync"
	"time"
)

// A first filter per visitor address, in memory only (never on disk): a
// bucket of Burst requests refilled at Rate a second. Generous (a home page
// asks for many images at once, a video for a segment every few seconds):
// the backend's own limits stay the real ones. Past it: 429.
const (
	Rate  = 50
	Burst = 400
	// Addresses not seen for this long are forgotten.
	forgetAfter = 10 * time.Minute
)

type bucket struct {
	tokens float64
	last   time.Time
}

type limiter struct {
	mu      sync.Mutex
	buckets map[netip.Addr]*bucket
	swept   time.Time
	now     func() time.Time
}

func (l *limiter) allow(a netip.Addr) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if l.buckets == nil {
		l.buckets = map[netip.Addr]*bucket{}
	}
	if now.Sub(l.swept) > forgetAfter {
		for k, b := range l.buckets {
			if now.Sub(b.last) > forgetAfter {
				delete(l.buckets, k)
			}
		}
		l.swept = now
	}
	b, ok := l.buckets[a]
	if !ok {
		b = &bucket{tokens: Burst, last: now}
		l.buckets[a] = b
	}
	b.tokens = min(Burst, b.tokens+now.Sub(b.last).Seconds()*Rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Limit refuses a visitor past the limit (after withClientAddr resolved
// who they are).
func Limit(next http.Handler) http.Handler {
	l := &limiter{now: time.Now}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ap, err := netip.ParseAddrPort(ClientAddr(r))
		if err == nil && !l.allow(ap.Addr().Unmap()) {
			w.Header().Set("Retry-After", "5")
			http.Error(w, "Too many requests. Try again in a moment.", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
