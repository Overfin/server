package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/overfin/server/internal/edge"
	"github.com/overfin/server/internal/link"
)

// Cache keeps images on the gateway's disk, so most don't cross the link
// again. Only what isn't personal: images (artwork, posters, photos),
// whose URLs change whenever the image does (immutable). Rule 39 holds:
// before serving a cached image, the gateway asks the backend whether this
// session may have it (link.CheckOnly: the access check, no bytes), and
// keeps the answer in memory a few minutes. How it chooses what to keep:
//   - under its limit, and leaving a share of the disk free, shrinking when
//     other programs need room;
//   - by worth: how often a file is asked for, against its size and how
//     long it took through the link; large, rarely used files go first;
//   - what many sessions ask for (the home page's slideshow and first rows,
//     whatever they are on this server) is kept even under pressure;
//   - nothing is ever stale: a changed image is a new URL.
type Cache struct {
	Dir    string
	Limit  int64   // bytes, at most
	Margin float64 // share of the disk left free (0.10)
	Logger *slog.Logger

	mu      sync.Mutex
	entries map[string]*entry
	used    int64
	allowed map[string]time.Time // session hash + key: until when it may have it
	// epoch counts ForgetAccess: an answer asked before one isn't kept.
	epoch    uint64
	variants map[string]map[string]bool // by image kind: the sizes asked for (queries)
	queue    chan string                // new images to fetch ahead
}

type entry struct {
	file     string
	size     int64
	ctype    string
	hits     float64 // decayed: halves every day
	at       time.Time
	fetch    time.Duration
	sessions map[[8]byte]time.Time // distinct sessions lately (up to essentialAt)
}

const (
	maxCached   = 16 << 20
	allowedFor  = 5 * time.Minute
	essentialAt = 5 // distinct sessions within a day: essential
	halfLife    = 24 * time.Hour
)

// cacheable says whether a request may be answered from the cache.
func cacheable(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/images/") && r.Header.Get("Range") == ""
}

func key(r *http.Request) string { return r.URL.Path + "?" + r.URL.RawQuery }

func (c *Cache) path(k string) string {
	sum := sha256.Sum256([]byte(k))
	h := hex.EncodeToString(sum[:])
	return filepath.Join(c.Dir, h[:2], h)
}

// session is a short hash of the visitor's login cookie: the cache's own
// in-memory key, never on disk.
func session(r *http.Request) ([8]byte, bool) {
	login, _, ok := edge.Pair(r)
	if !ok {
		return [8]byte{}, false
	}
	sum := sha256.Sum256([]byte("cache " + login))
	return [8]byte(sum[:8]), true
}

// Wrap answers cacheable requests from the cache when it can, filling it
// from the backend's answers; next is the way to the backend.
func (c *Cache) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, signedIn := session(r)
		if !cacheable(r) || !signedIn {
			next.ServeHTTP(w, r)
			return
		}
		k := key(r)
		if e, ok := c.lookup(k); ok {
			if c.may(next, r, sess, k) {
				if f, err := os.Open(e.file); err == nil {
					defer f.Close()
					c.used1(k, sess)
					h := w.Header()
					h.Set("Content-Type", e.ctype)
					h.Set("Cache-Control", "private, max-age=604800, immutable")
					h.Set("Content-Length", strconv.FormatInt(e.size, 10))
					h.Set("X-Content-Type-Options", "nosniff")
					_, _ = io.Copy(w, f)
					return
				}
			} else {
				notFound(w)
				return
			}
		}
		// Not cached: from the backend, kept on the way if it's worth it.
		start := time.Now()
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)
		res := rec.Result()
		body := rec.Body.Bytes()
		for name, vals := range res.Header {
			w.Header()[name] = vals
		}
		w.WriteHeader(res.StatusCode)
		_, _ = w.Write(body)
		if res.StatusCode == http.StatusOK && strings.Contains(res.Header.Get("Cache-Control"), "immutable") && len(body) <= maxCached {
			c.store(k, body, res.Header.Get("Content-Type"), time.Since(start), sess)
		}
	})
}

// may asks the backend whether this session may have k (the image's own
// request, with CheckOnly), unless it said yes in the last minutes.
func (c *Cache) may(next http.Handler, r *http.Request, sess [8]byte, k string) bool {
	ak := string(sess[:]) + k
	c.mu.Lock()
	until, ok := c.allowed[ak]
	epoch := c.epoch
	c.mu.Unlock()
	if ok && time.Now().Before(until) {
		return true
	}
	check := r.Clone(r.Context())
	check.Header.Set(link.CheckOnly, "1")
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, check)
	if rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
		return false
	}
	c.mu.Lock()
	if c.allowed == nil {
		c.allowed = map[string]time.Time{}
	}
	if len(c.allowed) > 200000 {
		now := time.Now()
		for k, t := range c.allowed {
			if now.After(t) {
				delete(c.allowed, k)
			}
		}
	}
	// Allowed now; kept unless asked before a change.
	if c.epoch == epoch {
		c.allowed[ak] = time.Now().Add(allowedFor)
	}
	c.mu.Unlock()
	return true
}

// ForgetAccess drops every access answer kept: what a profile may see
// changed (the backend's "access" event), or a change may have been missed
// (the events stream started again). Each image is checked again.
func (c *Cache) ForgetAccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.allowed = nil
	c.epoch++
}

func (c *Cache) lookup(k string) (*entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[k]
	return e, ok
}

// used1 counts a use of k by sess.
func (c *Cache) used1(k string, sess [8]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[k]; ok {
		e.touch(sess, time.Now())
	}
}

func (e *entry) touch(sess [8]byte, now time.Time) {
	e.hits = e.hits*decay(now.Sub(e.at)) + 1
	e.at = now
	if e.sessions == nil {
		e.sessions = map[[8]byte]time.Time{}
	}
	for s, t := range e.sessions {
		if now.Sub(t) > 24*time.Hour {
			delete(e.sessions, s)
		}
	}
	if len(e.sessions) < essentialAt {
		e.sessions[sess] = now
	}
}

func decay(d time.Duration) float64 {
	if d <= 0 {
		return 1
	}
	f := 1.0
	for d > halfLife {
		f /= 2
		d -= halfLife
	}
	return f * (1 - 0.5*float64(d)/float64(halfLife))
}

// essential: many sessions asked for it within a day.
func (e *entry) essential() bool { return len(e.sessions) >= essentialAt }

// worth: often asked for, slow to fetch, small.
func (e *entry) worth(now time.Time) float64 {
	return e.hits * decay(now.Sub(e.at)) * (float64(e.fetch.Milliseconds()) + 1) / float64(e.size+1)
}

func (c *Cache) store(k string, body []byte, ctype string, took time.Duration, sess [8]byte) {
	file := c.path(k)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return
	}
	tmp := file + ".new"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		os.Remove(tmp)
		c.Logger.Warn("cache: a file couldn't be written; passing through", "err", err)
		return
	}
	if err := os.Rename(tmp, file); err != nil {
		os.Remove(tmp)
		return
	}
	now := time.Now()
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[string]*entry{}
	}
	if old, ok := c.entries[k]; ok {
		c.used -= old.size
	}
	e := &entry{file: file, size: int64(len(body)), ctype: ctype, fetch: took, at: now}
	if sess != ([8]byte{}) {
		e.touch(sess, now)
	} else {
		e.hits = 0.5 // fetched ahead: worth little until someone asks
	}
	c.entries[k] = e
	if path, query, ok := strings.Cut(k, "?"); ok {
		if kind := imageKind(path); kind != "" {
			if c.variants == nil {
				c.variants = map[string]map[string]bool{}
			}
			if c.variants[kind] == nil {
				c.variants[kind] = map[string]bool{}
			}
			if len(c.variants[kind]) < maxVariants {
				c.variants[kind][query] = true
			}
		}
	}
	c.used += e.size
	c.mu.Unlock()
	c.fit()
}

// budget is how much the cache may take now: its limit, and no more than
// leaves the disk's margin free.
func (c *Cache) budget(used int64) int64 {
	b := c.Limit
	var st syscall.Statfs_t
	if err := syscall.Statfs(c.Dir, &st); err == nil {
		total := int64(st.Blocks) * int64(st.Bsize)
		free := int64(st.Bavail) * int64(st.Bsize)
		b = min(b, used+free-int64(c.Margin*float64(total)))
	}
	return max(b, 0)
}

// fit drops the least worth until the cache fits its budget; what's
// essential goes last.
func (c *Cache) fit() {
	c.mu.Lock()
	defer c.mu.Unlock()
	budget := c.budget(c.used)
	now := time.Now()
	for c.used > budget && len(c.entries) > 0 {
		var worst string
		worstWorth, worstEssential := 0.0, true
		for k, e := range c.entries {
			ess := e.essential()
			w := e.worth(now)
			if worst == "" || (!ess && worstEssential) || (ess == worstEssential && w < worstWorth) {
				worst, worstWorth, worstEssential = k, w, ess
			}
		}
		e := c.entries[worst]
		os.Remove(e.file)
		c.used -= e.size
		delete(c.entries, worst)
	}
}

// Load reads back what the cache holds on disk (after a restart): its
// files are kept, their worth starts again.
func (c *Cache) Load() {
	_ = os.MkdirAll(c.Dir, 0o700)
	// The cache's index is in memory only (it names what was asked for):
	// files from before a restart are cleared rather than guessed at.
	entries, _ := os.ReadDir(c.Dir)
	for _, d := range entries {
		_ = os.RemoveAll(filepath.Join(c.Dir, d.Name()))
	}
}

// Used is how many bytes the cache holds, and how many files.
func (c *Cache) Used() (int64, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used, len(c.entries)
}

// Fetching ahead: when the backend says titles came (its events), the
// gateway fetches their artwork into the cache, one at a time, at the sizes
// browsers asked for that kind of image here, so the first visitor doesn't
// wait. Best effort: a full queue drops the rest.
const (
	maxVariants = 6
	aheadPause  = 300 * time.Millisecond
	aheadQueue  = 2000
)

// imageKind is a TMDB artwork route's kind (poster, backdrop, logo…), ""
// for other images.
func imageKind(path string) string {
	id := strings.TrimPrefix(path, "/api/v1/images/")
	rest, ok := strings.CutPrefix(id, "tmdbart_")
	if !ok {
		return ""
	}
	kind, _, _ := strings.Cut(rest, "_")
	return kind
}

// Ahead queues new images (their routes, no size) to fetch ahead.
func (c *Cache) Ahead(paths []string) {
	c.mu.Lock()
	q := c.queue
	c.mu.Unlock()
	if q == nil {
		return
	}
	for _, p := range paths {
		select {
		case q <- p:
		default:
			return
		}
	}
}

// FetchAhead fetches what Ahead queues, from the backend over the link
// (its prefetch route), until ctx ends.
func (c *Cache) FetchAhead(ctx context.Context, backend http.RoundTripper) {
	q := make(chan string, aheadQueue)
	c.mu.Lock()
	c.queue = q
	c.mu.Unlock()
	client := &http.Client{Transport: backend, Timeout: time.Minute}
	for {
		var path string
		select {
		case <-ctx.Done():
			return
		case path = <-q:
		}
		kind := imageKind(path)
		c.mu.Lock()
		var queries []string
		for v := range c.variants[kind] {
			if _, cached := c.entries[path+"?"+v]; !cached {
				queries = append(queries, v)
			}
		}
		c.mu.Unlock()
		for _, v := range queries {
			start := time.Now()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://backend/link/v1/artwork/"+strings.TrimPrefix(path, "/api/v1/images/")+"?"+v, nil)
			resp, err := client.Do(req)
			if err != nil {
				break
			}
			body, err := io.ReadAll(io.LimitReader(resp.Body, maxCached+1))
			resp.Body.Close()
			if err == nil && resp.StatusCode == http.StatusOK && len(body) <= maxCached {
				c.store(path+"?"+v, body, resp.Header.Get("Content-Type"), time.Since(start), [8]byte{})
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(aheadPause):
			}
		}
	}
}

// queueReady says whether FetchAhead runs (tests).
func (c *Cache) queueReady() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.queue != nil
}
