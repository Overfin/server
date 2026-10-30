package api_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// lockedBuffer is a log the server may write from several goroutines.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// A browser that leaves before the answer isn't an error in the log: its
// requests fail with context canceled from the first lookup on.
func TestClientThatLeftIsNotAnError(t *testing.T) {
	log := &lockedBuffer{}
	srv, _ := newServerWith(t, options{log: log})
	if code := get(t, srv, "/api/v1/titles?kind=movie&limit=3", nil); code != http.StatusOK {
		t.Fatalf("a request that stays: %d", code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, path := range []string{"/api/v1/titles?kind=movie&limit=3", "/api/v1/titles?kind=series&limit=3"} {
		req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
		req.RemoteAddr = "127.0.0.1:40000"
		req.Header.Set("Remote-User", subject)
		srv.Config.Handler.ServeHTTP(httptest.NewRecorder(), req)
	}
	if strings.Contains(log.String(), "level=ERROR") {
		t.Fatalf("logged as an error:\n%s", log.String())
	}
}
