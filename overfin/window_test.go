package overfin

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestJellyfinWindow(t *testing.T) {
	jf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "Jellyfin's own page") }))
	defer jf.Close()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	jellyfinWindowAddr = ln.Addr().String()
	ln.Close()
	t.Cleanup(func() { jellyfinWindowAddr = ":8082" })
	var open atomic.Bool
	access := func(context.Context) (bool, time.Time, error) { return open.Load(), time.Time{}, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go jellyfinWindow(ctx, access, jf.URL, slog.New(slog.NewTextHandler(io.Discard, nil)))

	get := func() (string, error) {
		resp, err := http.Get("http://" + jellyfinWindowAddr + "/")
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b), nil
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := get(); err == nil {
		t.Fatal("served while access is closed")
	}
	open.Store(true)
	deadline := time.Now().Add(8 * time.Second)
	var body string
	for time.Now().Before(deadline) {
		if b, err := get(); err == nil {
			body = b
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if body != "Jellyfin's own page" {
		t.Fatalf("while open: %q", body)
	}
	open.Store(false)
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := get(); err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Error("still served after closing")
}
