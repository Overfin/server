package link

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The backend answers the gateway beside it only at its own version, and
// takes the visitor's address it gives.
func TestLocal(t *testing.T) {
	h := Local("1.0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.RemoteAddr+" "+r.Header.Get(ClientAddr)+r.Header.Get(LocalVersion)+r.Header.Get(CheckOnly))
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(LocalVersion, "1.0")
	req.Header.Set(ClientAddr, "192.168.1.5:4000")
	req.Header.Set(CheckOnly, "1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.String() != "192.168.1.5:4000 " {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	req.Header.Set(LocalVersion, "2.0")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 503 || rec.Header().Get(LocalVersion) != "1.0" || rec.Body.Len() != 0 {
		t.Fatalf("other version: %d %q", rec.Code, rec.Header())
	}
}

// Unpair forgets the gateway, and says where it was.
func TestUnpair(t *testing.T) {
	b, err := NewBackend(t.TempDir(), "1.0", http.NotFoundHandler(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if addr, err := b.Unpair(); err != nil || addr != "" {
		t.Fatalf("none paired: %q %v", addr, err)
	}
	if _, err := b.Pair("gateway:8444", ""); err != nil {
		t.Fatal(err)
	}
	if addr, _ := b.Address(); addr != "gateway:8444" {
		t.Fatalf("address: %q", addr)
	}
	if addr, err := b.Unpair(); err != nil || addr != "gateway:8444" {
		t.Fatalf("unpair: %q %v", addr, err)
	}
	if paired, pending, _ := b.State(); paired || pending {
		t.Fatal("still paired")
	}
}
