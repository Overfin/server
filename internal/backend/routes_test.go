package backend

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// What the backend answers its gateway: its version; a stream of zeros
// of the length asked, 1 byte to 10 GiB, for the link's speed; nothing
// else, and only GETs.
func TestLinkRoutes(t *testing.T) {
	srv := httptest.NewServer(Routes())
	defer srv.Close()
	get := func(path string) (int, []byte, http.Header) {
		t.Helper()
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b, res.Header
	}
	code, body, _ := get("/link/v1/status")
	var status struct{ Version string }
	if json.Unmarshal(body, &status); code != 200 || status.Version != Version {
		t.Errorf("status: %d %s", code, body)
	}
	code, body, h := get("/link/v1/speed?bytes=200000")
	if code != 200 || len(body) != 200000 || h.Get("Content-Length") != "200000" || h.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("speed: %d %d %v", code, len(body), h)
	}
	for _, q := range []string{"", "bytes=0", "bytes=-1", "bytes=x", "bytes=10737418241"} {
		if code, _, _ := get("/link/v1/speed?" + q); code != 400 {
			t.Errorf("speed %q: %d", q, code)
		}
	}
	if code, _, _ := get("/link/v1/other"); code != 404 {
		t.Errorf("another route: %d", code)
	}
	res, _ := http.Post(srv.URL+"/link/v1/status", "text/plain", nil)
	if res.StatusCode != 405 {
		t.Errorf("a POST: %d", res.StatusCode)
	}
}

// Jellyfin is up only once its /health says "Healthy": its temporary
// server's "Degraded", and the 503 while it loads, are waited through;
// the wait ends with its context.
func TestWaitHealthy(t *testing.T) {
	var asks atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch asks.Add(1) {
		case 1:
			io.WriteString(w, "Degraded")
		case 2:
			w.WriteHeader(503)
			io.WriteString(w, "Healthy")
		default:
			io.WriteString(w, "Healthy\n")
		}
	}))
	defer srv.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := WaitHealthy(ctx, srv.URL, logger); err != nil || asks.Load() != 3 {
		t.Errorf("%v after %d asks", err, asks.Load())
	}
	never := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "Degraded") }))
	defer never.Close()
	short, cancel2 := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel2()
	if err := WaitHealthy(short, never.URL, logger); err != context.DeadlineExceeded {
		t.Errorf("never healthy: %v", err)
	}
}
