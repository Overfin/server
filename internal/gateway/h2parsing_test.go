package gateway

import (
	"crypto/tls"
	"encoding/binary"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// h2Field is one header of a request written by hand: Go's own client
// won't send a malformed one.
type h2Field struct{ name, value string }

// hpackLiteral writes fields as HPACK literals, new names, never indexed,
// no Huffman: the plainest encoding, which every decoder must read.
func hpackLiteral(fields []h2Field) []byte {
	var b []byte
	str := func(s string) {
		if len(s) < 127 {
			b = append(b, byte(len(s)))
		} else { // a 7-bit prefix integer, then the rest 7 bits at a time
			b = append(b, 127)
			for n := len(s) - 127; ; n >>= 7 {
				if n < 128 {
					b = append(b, byte(n))
					break
				}
				b = append(b, byte(n&127|128))
			}
		}
		b = append(b, s...)
	}
	for _, f := range fields {
		b = append(b, 0x10) // literal, never indexed, new name
		str(f.name)
		str(f.value)
	}
	return b
}

func h2Frame(typ, flags byte, stream uint32, payload []byte) []byte {
	h := make([]byte, 9, 9+len(payload))
	h[0], h[1], h[2] = byte(len(payload)>>16), byte(len(payload)>>8), byte(len(payload))
	h[3], h[4] = typ, flags
	binary.BigEndian.PutUint32(h[5:], stream)
	return append(h, payload...)
}

// h2Ask sends one request on a new HTTP/2 connection and says how it
// ended: "status NNN" (the common statuses HPACK indexes; "status" for
// others), "reset" (the stream refused) or "goaway" (the connection).
func h2Ask(t *testing.T, addr string, fields []h2Field) string {
	t.Helper()
	c, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	io.WriteString(c, "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")
	c.Write(h2Frame(0x4, 0, 0, nil))                    // SETTINGS
	c.Write(h2Frame(0x1, 0x5, 1, hpackLiteral(fields))) // HEADERS, END_STREAM | END_HEADERS
	head := make([]byte, 9)
	for {
		if _, err := io.ReadFull(c, head); err != nil {
			return "closed"
		}
		n := int(head[0])<<16 | int(head[1])<<8 | int(head[2])
		payload := make([]byte, n)
		if _, err := io.ReadFull(c, payload); err != nil {
			return "closed"
		}
		stream := binary.BigEndian.Uint32(head[5:]) & 0x7fffffff
		switch typ := head[3]; {
		case typ == 0x7:
			return "goaway"
		case typ == 0x3 && stream == 1:
			return "reset"
		case typ == 0x1 && stream == 1 && n > 0:
			if s, ok := map[byte]string{0x88: "200", 0x89: "204", 0x8a: "206", 0x8b: "304", 0x8c: "400", 0x8d: "404", 0x8e: "500"}[payload[0]]; ok {
				return "status " + s
			}
			return "status"
		}
	}
}

// Requests over HTTP/2 with their pseudo-headers wrong, missing, twice,
// out of place, or with headers HTTP/2 forbids: each is refused, the
// stream or the connection, or answered at the gateway; none reaches the
// backend but as one clean request.
func TestRequestParsingHTTP2(t *testing.T) {
	b := &keptBackend{fakeBackend: fakeBackend{connected: true}}
	srv := httptest.NewUnstartedServer(Handler(b, "1.0", nil, nil, quiet))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "https://")
	req := func(method, path string, more ...h2Field) []h2Field {
		return append([]h2Field{{":method", method}, {":scheme", "https"}, {":authority", "x"}, {":path", path}}, more...)
	}
	refused := []string{"reset", "goaway", "status 400", "status 404", "status"}
	for _, c := range []struct {
		name    string
		fields  []h2Field
		answers []string // any of them
		reached string   // the path the backend got, if any
	}{
		{"a plain request", req("GET", "/"), []string{"status 200"}, "/"},
		{"no :path", []h2Field{{":method", "GET"}, {":scheme", "https"}, {":authority", "x"}}, refused, ""},
		{"an empty :path", req("GET", ""), refused, ""},
		{"no :method", []h2Field{{":scheme", "https"}, {":authority", "x"}, {":path", "/"}}, refused, ""},
		{"no :scheme", []h2Field{{":method", "GET"}, {":authority", "x"}, {":path", "/"}}, refused, ""},
		{":path without its slash", req("GET", "admin/"), refused, ""},
		{":path as an asterisk", req("GET", "*"), refused, ""},
		{":path to the panel", req("GET", "/admin/"), []string{"status 404"}, ""},
		{":path with an encoded slash", req("GET", "/%2fadmin/"), []string{"status 404"}, ""},
		{":path with dots", req("GET", "/api/v1/../../admin/"), refused, ""},
		{":path naming a host", req("GET", "http://elsewhere.example/admin/"), refused, ""},
		{"two :paths", append(req("GET", "/"), h2Field{":path", "/admin/"}), refused, ""},
		{"two :methods", append(req("GET", "/"), h2Field{":method", "DELETE"}), refused, ""},
		{"an unknown pseudo-header", req("GET", "/", h2Field{":admin", "1"}), refused, ""},
		{"a response's pseudo-header", req("GET", "/", h2Field{":status", "200"}), refused, ""},
		{"a pseudo-header after a header", append([]h2Field{{":method", "GET"}, {":scheme", "https"}, {"x-a", "1"}, {":authority", "x"}}, h2Field{":path", "/admin/"}), refused, ""},
		{"a capital in a name", req("GET", "/", h2Field{"X-Upper", "1"}), refused, ""},
		{"Connection", req("GET", "/", h2Field{"connection", "close"}), refused, ""},
		{"Upgrade", req("GET", "/", h2Field{"upgrade", "h2c"}), refused, ""},
		{"Transfer-Encoding", req("GET", "/", h2Field{"transfer-encoding", "chunked"}), refused, ""},
		{"TE other than trailers", req("GET", "/", h2Field{"te", "gzip"}), refused, ""},
		{"a line break in a value", req("GET", "/", h2Field{"x-a", "1\r\nx-b: 2"}), refused, ""},
		{"CONNECT", []h2Field{{":method", "CONNECT"}, {":authority", "elsewhere.example:443"}}, refused, ""},
		{":authority and Host apart", req("GET", "/", h2Field{"host", "elsewhere.example"}), []string{"status 200", "reset", "status 400"}, "/"},
		{"the gateway's own headers", req("GET", "/", h2Field{"overfin-client-addr", "192.0.2.1"}, h2Field{"overfin-client-addr", "192.0.2.2"}, h2Field{"overfin-check", "1"}, h2Field{"x-forwarded-for", "192.0.2.3"}), []string{"status 200"}, "/"},
	} {
		b.mu.Lock()
		before := len(b.got)
		b.mu.Unlock()
		got := h2Ask(t, addr, c.fields)
		time.Sleep(20 * time.Millisecond) // a request the backend got is kept by now
		b.mu.Lock()
		sent := b.got[before:]
		b.mu.Unlock()
		ok := false
		for _, a := range c.answers {
			ok = ok || got == a
		}
		if !ok {
			t.Errorf("%s: %s, want one of %v", c.name, got, c.answers)
		}
		var paths []string
		for _, r := range sent {
			paths = append(paths, r.URL.Path)
			if v := r.Header.Values("Overfin-Client-Addr"); len(v) != 1 || strings.HasPrefix(v[0], "192.0.2.") {
				t.Errorf("%s: the backend was told the visitor is %v", c.name, v)
			}
			for _, h := range []string{"Overfin-Check", "X-Forwarded-For", "Upgrade", "Connection", "Te"} {
				if v := r.Header.Values(h); len(v) > 0 {
					t.Errorf("%s: the backend got %s: %v", c.name, h, v)
				}
			}
		}
		if want := c.reached; strings.Join(paths, " ") != want && !(want != "" && got != "status 200" && len(paths) == 0) {
			t.Errorf("%s: the backend got %v, want %q", c.name, paths, want)
		}
	}
}
