package jellyfin

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// serverFrame is a frame as a server sends it: unmasked.
func serverFrame(opcode byte, fin bool, payload string) []byte {
	b := byte(opcode)
	if fin {
		b |= 0x80
	}
	return append([]byte{b, byte(len(payload))}, payload...)
}

// readClientFrame reads one frame a client sent, unmasking it.
func readClientFrame(r *bufio.Reader) (byte, string, error) {
	var head [2]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, "", err
	}
	if head[1]&0x80 == 0 {
		return 0, "", io.ErrUnexpectedEOF // unmasked: wrong
	}
	n := int(head[1] & 0x7F)
	var mask [4]byte
	io.ReadFull(r, mask[:])
	p := make([]byte, n)
	io.ReadFull(r, p)
	for i := range p {
		p[i] ^= mask[i%4]
	}
	return head[0] & 0x0F, string(p), nil
}

// Library changes come in, whole even when sent in pieces; pings get
// their pong, and Overfin says it's alive, masked, as a client must.
func TestWatch(t *testing.T) {
	got := make(chan string, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/socket" || !strings.Contains(r.Header.Get("Authorization"), `Token="tok"`) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + websocketGUID))
		conn, rw, _ := w.(http.Hijacker).Hijack()
		defer conn.Close()
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
			base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
		rw.Flush()
		if _, p, err := readClientFrame(rw.Reader); err != nil || !strings.Contains(p, "KeepAlive") {
			got <- "no keep-alive"
			return
		}
		conn.Write(serverFrame(0x9, true, "hi"))
		if op, p, _ := readClientFrame(rw.Reader); op != 0xA || p != "hi" {
			got <- "no pong"
			return
		}
		conn.Write(serverFrame(0x1, true, `{"MessageType":"UserDataChanged","Data":{}}`))
		conn.Write(serverFrame(0x1, false, `{"MessageType":"LibraryChanged",`))
		conn.Write(serverFrame(0x0, true, `"Data":{"ItemsAdded":["a1"],"ItemsRemoved":["r1","r2"]}}`))
		conn.Write(serverFrame(0x8, true, ""))
		readClientFrame(rw.Reader) // the close's answer
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "test")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var changes []LibraryChange
	in := false
	err := c.Watch(ctx, Auth{Token: "tok", DeviceID: "d"}, func() { in = true }, func(lc LibraryChange) { changes = append(changes, lc) })
	select {
	case s := <-got:
		t.Fatal(s)
	default:
	}
	if err == nil || !in || len(changes) != 1 || changes[0].ItemsAdded[0] != "a1" || len(changes[0].ItemsRemoved) != 2 {
		t.Fatalf("watch: %v %v %+v", err, in, changes)
	}
	// Refused: says so.
	if err := c.Watch(ctx, Auth{Token: "bad"}, func() {}, func(LibraryChange) {}); !IsStatus(err, 401) {
		t.Errorf("refused: %v", err)
	}
}

// upgraded answers the handshake, then hands the connection to talk.
func upgraded(talk func(conn net.Conn, r *bufio.Reader)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + websocketGUID))
		conn, rw, _ := w.(http.Hijacker).Hijack()
		defer conn.Close()
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
			base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
		rw.Flush()
		talk(conn, rw.Reader)
	}))
}

// A socket that goes silent is dropped; one Jellyfin keeps talking on
// stays, past the silence allowed (rule 6).
func TestWatchSilence(t *testing.T) {
	was := socketSilence
	socketSilence = 200 * time.Millisecond
	defer func() { socketSilence = was }()
	c, _ := New("http://192.0.2.1", "test")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	silent := upgraded(func(conn net.Conn, r *bufio.Reader) {
		io.Copy(io.Discard, r) // reads, never answers
	})
	defer silent.Close()
	c.SetURL(silent.URL)
	start := time.Now()
	err := c.Watch(ctx, Auth{Token: "tok"}, func() {}, func(LibraryChange) {})
	if err == nil || !strings.Contains(err.Error(), "silent") || time.Since(start) > 2*time.Second {
		t.Errorf("silent: %v after %v", err, time.Since(start))
	}

	talking := upgraded(func(conn net.Conn, r *bufio.Reader) {
		go io.Copy(io.Discard, r)
		for range 8 {
			time.Sleep(100 * time.Millisecond)
			conn.Write(serverFrame(0x1, true, `{"MessageType":"KeepAlive"}`))
		}
		conn.Write(serverFrame(0x8, true, ""))
		time.Sleep(100 * time.Millisecond)
	})
	defer talking.Close()
	c.SetURL(talking.URL)
	start = time.Now()
	err = c.Watch(ctx, Auth{Token: "tok"}, func() {}, func(LibraryChange) {})
	if err == nil || strings.Contains(err.Error(), "silent") || time.Since(start) < 800*time.Millisecond {
		t.Errorf("talking: %v after %v", err, time.Since(start))
	}
}

// A Jellyfin that never answers the handshake doesn't hold Overfin.
func TestWatchHandshakeTimeout(t *testing.T) {
	tr := socketHTTP.Transport.(*http.Transport)
	was := tr.ResponseHeaderTimeout
	tr.ResponseHeaderTimeout = 200 * time.Millisecond
	defer func() { tr.ResponseHeaderTimeout = was }()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	c, _ := New(srv.URL, "test")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := c.Watch(ctx, Auth{Token: "tok"}, func() {}, func(LibraryChange) {}); err == nil || time.Since(start) > 2*time.Second {
		t.Errorf("no answer: %v after %v", err, time.Since(start))
	}
}
