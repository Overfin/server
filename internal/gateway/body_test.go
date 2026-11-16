package gateway

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A body arrives whole at the gateway before the request goes on; one
// that trickles past the time allowed is refused there, without reaching
// the backend; a body too large goes on cut one byte past the backend's
// cap, for the backend to refuse as before.
func TestWholeBody(t *testing.T) {
	bodyTime = time.Second
	t.Cleanup(func() { bodyTime = 20 * time.Second })
	var got []string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, fmt.Sprintf("%d/%d", len(b), r.ContentLength))
	})
	srv := &http.Server{Handler: wholeBody(next)}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	url := "http://" + ln.Addr().String() + "/x"

	if resp, err := http.Post(url, "application/json", strings.NewReader(`{"a":1}`)); err != nil || resp.StatusCode != 200 {
		t.Fatalf("small body: %v %v", resp, err)
	}
	if resp, err := http.Post(url, "application/json", bytes.NewReader(make([]byte, maxBody+500))); err != nil || resp.StatusCode != 200 {
		t.Fatalf("large body: %v %v", resp, err)
	}
	if strings.Join(got, ",") != fmt.Sprintf("7/7,%d/%d", maxBody+1, maxBody+1) {
		t.Errorf("passed on: %v", got)
	}

	// A trickle: headers, then nothing.
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "POST /x HTTP/1.1\r\nHost: x\r\nContent-Length: 60000\r\n\r\n{")
	c.SetReadDeadline(time.Now().Add(bodyTime + 10*time.Second))
	buf := make([]byte, 64)
	n, _ := c.Read(buf)
	if !strings.HasPrefix(string(buf[:n]), "HTTP/1.1 400") || len(got) != 2 {
		t.Errorf("a trickle: %q, passed on %v", buf[:n], got)
	}
}
