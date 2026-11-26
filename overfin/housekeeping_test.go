package overfin

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

// logged keeps what a logger wrote.
type logged struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logged) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logged) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// The hourly housekeeping's first pass, on a database as Overfin makes
// it: every step goes through (none logs a failure), and it stops with
// its context.
func TestHousekeepingPass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	box, _ := seal.New(bytes.Repeat([]byte{6}, 32))
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "o.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.CreateUser(ctx, "never-confirmed@example.org", false)
	log := &logged{}
	done := make(chan struct{})
	go func() {
		purgeExpired(ctx, db, slog.New(slog.NewTextHandler(log, nil)))
		close(done)
	}()
	time.Sleep(500 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("it didn't stop with its context")
	}
	if strings.Contains(log.String(), "failed") || strings.Contains(log.String(), "level=ERROR") {
		t.Errorf("a step failed:\n%s", log)
	}
}

// After a lost secret key: what it sealed forgotten, the new key's check
// kept.
func TestForgetOldKey(t *testing.T) {
	ctx := context.Background()
	box, _ := seal.New(bytes.Repeat([]byte{6}, 32))
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "o.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	acct, owner, _ := db.CreateAccount(ctx, "s", store.NewOwner{Name: "A"})
	db.SetPIN(ctx, acct.ID, owner.ID, []byte("hash"))
	log := &logged{}
	if err := forgetOldKey(ctx, slog.New(slog.NewTextHandler(log, nil)), db, []byte("new check")); err != nil {
		t.Fatal(err)
	}
	if p, _ := db.Profile(ctx, acct.ID, owner.ID); p == nil || p.HasPIN {
		t.Errorf("the PIN after recovery: %+v", p)
	}
	if !strings.Contains(log.String(), "pins=1") {
		t.Errorf("logged: %s", log)
	}
	if err := db.CheckKey(ctx, []byte("new check")); err != nil {
		t.Errorf("the new key's check: %v", err)
	}
	if err := db.CheckKey(ctx, []byte("old check")); err == nil {
		t.Error("another key's check taken")
	}
}

// Every answer of a site served over HTTPS says so for a year; the
// control panel's paths (and the apps') go to the panel, the rest to the
// site; a swap serves what it holds now.
func TestSiteHandlers(t *testing.T) {
	who := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(name)) })
	}
	h := hsts(withPanel(who("site"), who("panel")))
	for path, want := range map[string]string{"/": "site", "/movies": "site", "/admin/": "panel", "/admin/api/x": "panel",
		"/apps/v1/titles": "panel", "/administrator": "site", "/api/v1/me": "site"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Body.String() != want || rec.Header().Get("Strict-Transport-Security") != "max-age=31536000" {
			t.Errorf("%s: %q %v", path, rec.Body, rec.Header())
		}
	}
	s := newSwap(settingUp)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 503 || rec.Header().Get("Retry-After") != "30" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("setting up: %d %v", rec.Code, rec.Header())
	}
	s.set(who("ready"))
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Body.String() != "ready" {
		t.Errorf("after the swap: %q", rec.Body)
	}
}
