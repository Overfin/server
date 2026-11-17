package federation

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/overfin/server/internal/slots"
	"github.com/overfin/server/internal/store"
)

// pairedPair is A and B paired, A offering B its films.
func pairedPair(t *testing.T) (a, b *server, bOnA, aOnB *store.Peer) {
	t.Helper()
	ctx := context.Background()
	a, b = newServer(t, "A"), newServer(t, "B")
	libraries(t, a, `{"Id":"films","Name":"Films","CollectionType":"movies"}`)
	a.s.Store.PutCatalog(ctx, []store.CatalogItem{{ID: "00000000000000000000000000000001", Kind: "movie", Name: "Heat",
		SortName: "heat", TMDB: "949", LibraryID: "films"}}, 1)
	a.s.Store.RefreshTitleKeys(ctx)
	code, _, _ := a.s.NewCode(ctx, "")
	pending, err := b.s.UseCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	bOnA, err = a.s.Accept(ctx, onlyPeer(t, a).ID)
	if err != nil {
		t.Fatal(err)
	}
	aOnB, _ = b.s.Store.PeerByID(ctx, pending.ID)
	if err := a.s.SetOffers(ctx, bOnA, []string{"films"}); err != nil {
		t.Fatal(err)
	}
	if err := b.s.SetWants(ctx, aOnB, []string{"films"}); err != nil {
		t.Fatal(err)
	}
	return a, b, bOnA, aOnB
}

// clock is a test's clock for a server's limits: still until moved.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) move(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// stillClock stops the clock of s's limits, refills included.
func stillClock(s *server) *clock {
	c := &clock{t: time.Now()}
	s.s.lim().Now = c.now
	return c
}

// drain empties a bucket of A's limits.
func drain(l *Limits, pick func(*Limits) *bucket) {
	l.mu.Lock()
	defer l.mu.Unlock()
	pick(l).tokens, pick(l).last = 0, l.now()
}

// A peer over its own limit gets a signed 429 rate-limited, the server
// over all peers' a signed 503 server-busy, each with retryAfter; the
// caller then waits that long before it calls again, and its next call
// goes once the time is up (MSFP core section 8.3).
func TestCallsOverTheLimits(t *testing.T) {
	ctx := context.Background()
	a, b, _, aOnB := pairedPair(t)
	keyB := b64.EncodeToString(b.s.Key.Public())
	keyA := b64.EncodeToString(a.s.Key.Public())
	onA := stillClock(a)

	drain(a.s.lim(), func(l *Limits) *bucket { return l.peers[keyB] })
	var answer librariesAnswer
	r, err := b.s.call(ctx, aOnB, "/api/v1/federation/libraries", struct{}{}, &answer)
	if err != nil || r.Status != http.StatusTooManyRequests || r.Problem != "rate-limited" || r.RetryAfter != 1 {
		t.Fatalf("over its own limit: %+v %v", r, err)
	}
	// Not again before retryAfter: nothing sent.
	if _, err := b.s.call(ctx, aOnB, "/api/v1/federation/libraries", struct{}{}, &answer); !errors.Is(err, ErrBackingOff) || !errors.Is(err, ErrUnreachable) {
		t.Fatalf("before retryAfter: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	onA.move(time.Second)
	if r, err := b.s.call(ctx, aOnB, "/api/v1/federation/libraries", struct{}{}, &answer); err != nil || r.Status != 200 {
		t.Fatalf("after retryAfter: %+v %v", r, err)
	}

	drain(a.s.lim(), func(l *Limits) *bucket { return l.all })
	r, err = b.s.call(ctx, aOnB, "/api/v1/federation/libraries", struct{}{}, &answer)
	if err != nil || r.Status != http.StatusServiceUnavailable || r.Problem != "server-busy" || r.RetryAfter != 1 {
		t.Fatalf("the server full: %+v %v", r, err)
	}
	// A caller waits for retryAfter on 503 too.
	if !b.s.lim().waits[keyA].retryAt.After(time.Now()) {
		t.Fatal("no wait kept after a 503")
	}
}

// A catalog read asked to wait a moment, mid-reading, waits, then reads
// on: never sooner (MSFP core section 8.3).
func TestSyncTitlesWaitsItsTurn(t *testing.T) {
	ctx := context.Background()
	a, b, _, aOnB := pairedPair(t)
	keyB := b64.EncodeToString(b.s.Key.Public())
	// One call left: libraries goes, titles waits about a second (A's
	// clock moves on once B is waiting).
	onA := stillClock(a)
	l := a.s.lim()
	l.mu.Lock()
	l.peers[keyB].tokens, l.peers[keyB].last = 1, l.now()
	l.mu.Unlock()
	go func() {
		time.Sleep(500 * time.Millisecond)
		onA.move(time.Second)
	}()
	start := time.Now()
	n, err := b.s.SyncTitles(ctx, aOnB)
	if err != nil || n != 1 || time.Since(start) < 900*time.Millisecond {
		t.Fatalf("%d %v after %v", n, err, time.Since(start))
	}
}

// Before any signature is checked, a network address over its limit gets
// the plain 404 of a failed check, unsigned: to a peer, no answer.
func TestCallsOverTheAddressLimit(t *testing.T) {
	ctx := context.Background()
	a, b, _, aOnB := pairedPair(t)
	stillClock(a)
	l := a.s.lim()
	l.mu.Lock()
	for k := range l.addrs {
		l.addrs[k].tokens, l.addrs[k].last = 0, l.now()
	}
	l.mu.Unlock()
	var answer librariesAnswer
	if _, err := b.s.call(ctx, aOnB, "/api/v1/federation/libraries", struct{}{}, &answer); !errors.Is(err, ErrUnreachable) || errors.Is(err, ErrBackingOff) {
		t.Fatalf("over the address limit: %v", err)
	}
	if ok := l.fromAddr(netip.MustParseAddr("192.0.2.1"), time.Now()); !ok {
		t.Fatal("another address")
	}
}

// Requests at once: past a peer's own, a 429; past all peers', a 503;
// each slot freed once its handler is done.
func TestCallsAtOnce(t *testing.T) {
	ctx := context.Background()
	a, b, _, aOnB := pairedPair(t)
	l := a.s.lim()
	l.mu.Lock()
	l.peerSlots = &slots.Slots{Size: 1, Wait: 10 * time.Millisecond}
	l.allSlots = &slots.Slots{Size: 1, Wait: 10 * time.Millisecond}
	l.mu.Unlock()
	var answer librariesAnswer
	if r, err := b.s.call(ctx, aOnB, "/api/v1/federation/libraries", struct{}{}, &answer); err != nil || r.Status != 200 {
		t.Fatalf("one: %+v %v", r, err)
	}
	if n := l.allSlots.InUse(""); n != 0 {
		t.Fatalf("slots still held: %d", n)
	}
	keyB := b64.EncodeToString(b.s.Key.Public())
	free, _ := l.peerSlots.Take(ctx, keyB)
	if r, err := b.s.call(ctx, aOnB, "/api/v1/federation/libraries", struct{}{}, &answer); err != nil || r.Status != http.StatusTooManyRequests || r.RetryAfter < 1 {
		t.Fatalf("its own at once: %+v %v", r, err)
	}
	free()
	b.s.lim().waits = map[string]*wait{}
	free, _ = l.allSlots.Take(ctx, "")
	if r, err := b.s.call(ctx, aOnB, "/api/v1/federation/libraries", struct{}{}, &answer); err != nil || r.Status != http.StatusServiceUnavailable {
		t.Fatalf("all at once: %+v %v", r, err)
	}
	free()
}

// play has a limit of its own, besides the pairing's streams.
func TestPlayLimit(t *testing.T) {
	var l Limits
	now := time.Now()
	for i := range playBurst {
		if no := l.call("k", true, now); no != nil {
			t.Fatalf("play %d: %+v", i, no)
		}
	}
	if no := l.call("k", true, now); no == nil || no.problem != "rate-limited" || no.retryAfter != 2 {
		t.Fatalf("past the burst: %+v", no)
	}
	if no := l.call("k", false, now); no != nil {
		t.Fatalf("other calls go on: %+v", no)
	}
	if no := l.call("k", true, now.Add(2*time.Second)); no != nil {
		t.Fatalf("2 seconds later: %+v", no)
	}
}

// A peer that doesn't answer: calls made in the background hold off,
// longer each time, up to an hour; a call someone asked for still goes.
func TestBackoffWhenNoAnswer(t *testing.T) {
	ctx := context.Background()
	_, b, _, aOnB := pairedPair(t)
	gone := *aOnB
	gone.Address = "http://127.0.0.1:1"
	var answer librariesAnswer
	bg := Background(ctx)
	if _, err := b.s.call(bg, &gone, "/api/v1/federation/libraries", struct{}{}, &answer); !errors.Is(err, ErrUnreachable) || errors.Is(err, ErrBackingOff) {
		t.Fatalf("first: %v", err)
	}
	if _, err := b.s.call(bg, &gone, "/api/v1/federation/libraries", struct{}{}, &answer); !errors.Is(err, ErrBackingOff) {
		t.Fatalf("in the background, at once: %v", err)
	}
	if _, err := b.s.call(ctx, &gone, "/api/v1/federation/libraries", struct{}{}, &answer); errors.Is(err, ErrBackingOff) {
		t.Fatalf("asked for: %v", err)
	}
	for n, want := range map[int][2]time.Duration{1: {1500 * time.Millisecond, 2500 * time.Millisecond},
		2: {3 * time.Second, 5 * time.Second}, 30: {45 * time.Minute, time.Hour}} {
		if d := backoff(n); d < want[0] || d > want[1] {
			t.Errorf("backoff(%d) = %v", n, d)
		}
	}
}

// retryAfter counts only from 1 to 3,600, as an integer.
func TestRetryAfterOf(t *testing.T) {
	for body, want := range map[string]int{`{"retryAfter":5}`: 5, `{"retryAfter":3600}`: 3600, `{"retryAfter":0}`: 0,
		`{"retryAfter":3601}`: 0, `{"retryAfter":1.5}`: 0, `{"retryAfter":"5"}`: 0, `{}`: 0, `x`: 0} {
		if got := retryAfterOf([]byte(body)); got != want {
			t.Errorf("%s: %d", body, got)
		}
	}
}
