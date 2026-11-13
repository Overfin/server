package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/overfin/server/internal/link"
)

// imageBackend serves images (immutable), counts full fetches and checks,
// and refuses what deny names.
type imageBackend struct {
	mu      sync.Mutex
	fetches map[string]int
	checks  map[string]int
	deny    map[string]bool
}

func (b *imageBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.deny[r.URL.Path] {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get(link.CheckOnly) != "" {
		b.checks[r.URL.Path]++
		w.WriteHeader(http.StatusNoContent)
		return
	}
	b.fetches[r.URL.Path]++
	w.Header().Set("Cache-Control", "private, max-age=604800, immutable")
	w.Header().Set("Content-Type", "image/jpeg")
	size := 1000
	if strings.Contains(r.URL.Path, "big") {
		size = 50000
	}
	w.Write([]byte(strings.Repeat("x", size)))
}

func get(h http.Handler, path, login string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Cookie", "overfin-login="+login+"; overfin-edge=x")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func newCache(t *testing.T, limit int64) (*Cache, *imageBackend, http.Handler) {
	c := &Cache{Dir: t.TempDir(), Limit: limit, Logger: quiet}
	c.Load()
	b := &imageBackend{fetches: map[string]int{}, checks: map[string]int{}, deny: map[string]bool{}}
	return c, b, c.Wrap(b)
}

func TestCacheServesWithCheck(t *testing.T) {
	_, b, h := newCache(t, 1<<30)
	if rec := get(h, "/api/v1/images/a?size=large", "L1"); rec.Code != 200 || rec.Body.Len() != 1000 {
		t.Fatalf("first: %d %d", rec.Code, rec.Body.Len())
	}
	// Another session: from the cache, after the backend said it may.
	rec := get(h, "/api/v1/images/a?size=large", "L2")
	if rec.Code != 200 || rec.Body.Len() != 1000 || b.fetches["/api/v1/images/a"] != 1 || b.checks["/api/v1/images/a"] != 1 {
		t.Fatalf("second: %d, fetched %d, checked %d", rec.Code, b.fetches["/api/v1/images/a"], b.checks["/api/v1/images/a"])
	}
	// The same session again: its answer is remembered.
	get(h, "/api/v1/images/a?size=large", "L2")
	if b.checks["/api/v1/images/a"] != 1 {
		t.Errorf("checked again: %d", b.checks["/api/v1/images/a"])
	}
	// Refused for a session: 404, though the bytes are cached.
	b.mu.Lock()
	b.deny["/api/v1/images/a"] = true
	b.mu.Unlock()
	if rec := get(h, "/api/v1/images/a?size=large", "L3"); rec.Code != http.StatusNotFound {
		t.Errorf("refused, from the cache: %d", rec.Code)
	}
}

func TestCacheNotSignedInPassesThrough(t *testing.T) {
	_, b, h := newCache(t, 1<<30)
	req := httptest.NewRequest("GET", "/api/v1/images/a", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if b.fetches["/api/v1/images/a"] != 2 {
		t.Errorf("without a session: fetched %d times", b.fetches["/api/v1/images/a"])
	}
}

// Past its limit, the cache drops the least worth first; what many
// sessions asked for stays.
func TestCacheEviction(t *testing.T) {
	c, _, h := newCache(t, 60000)
	// "popular": asked for by essentialAt sessions.
	for i := 0; i < essentialAt; i++ {
		get(h, "/api/v1/images/popular", "S"+strconv.Itoa(i))
	}
	get(h, "/api/v1/images/once", "S0")
	get(h, "/api/v1/images/big1", "S0") // 50,000 bytes
	get(h, "/api/v1/images/big2", "S0") // and 50,000 more: past the limit
	c.mu.Lock()
	_, popular := c.entries["/api/v1/images/popular?"]
	_, big1 := c.entries["/api/v1/images/big1?"]
	_, big2 := c.entries["/api/v1/images/big2?"]
	c.mu.Unlock()
	used, files := c.Used()
	if !popular || used > 60000 || (big1 && big2) {
		t.Errorf("popular kept %v, big ones kept %v %v, used %d in %d files", popular, big1, big2, used, files)
	}
}

// prefetchBackend serves the prefetch route, counting what it serves.
type prefetchBackend struct {
	mu     sync.Mutex
	served []string
}

func (p *prefetchBackend) RoundTrip(r *http.Request) (*http.Response, error) {
	p.mu.Lock()
	p.served = append(p.served, r.URL.Path+"?"+r.URL.RawQuery)
	p.mu.Unlock()
	h := http.Header{}
	h.Set("Cache-Control", "private, max-age=604800, immutable")
	h.Set("Content-Type", "image/jpeg")
	return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader("fetched ahead")), Request: r}, nil
}

func TestFetchAhead(t *testing.T) {
	c, b, h := newCache(t, 1<<30)
	// A browser asked for a poster at width 342: the cache learns the size.
	get(h, "/api/v1/images/tmdbart_poster_old1234567_jpg?width=342", "S1")
	pb := &prefetchBackend{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.FetchAhead(ctx, pb)
	for c.queueReady() == false {
		time.Sleep(10 * time.Millisecond)
	}
	c.Ahead([]string{"/api/v1/images/tmdbart_poster_new1234567_jpg"})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := c.lookup("/api/v1/images/tmdbart_poster_new1234567_jpg?width=342"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not fetched ahead; served %v", pb.served)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(pb.served) != 1 || pb.served[0] != "/link/v1/artwork/tmdbart_poster_new1234567_jpg?width=342" {
		t.Errorf("asked the backend for %v", pb.served)
	}
	// The first visitor gets it from the disk, after the check.
	rec := get(h, "/api/v1/images/tmdbart_poster_new1234567_jpg?width=342", "S2")
	if rec.Body.String() != "fetched ahead" || b.fetches["/api/v1/images/tmdbart_poster_new1234567_jpg"] != 0 || b.checks["/api/v1/images/tmdbart_poster_new1234567_jpg"] != 1 {
		t.Errorf("served %q, fetched %d, checked %d", rec.Body.String(), b.fetches["/api/v1/images/tmdbart_poster_new1234567_jpg"], b.checks["/api/v1/images/tmdbart_poster_new1234567_jpg"])
	}
}
