package gateway

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// keptBackend keeps every request the gateway sends it, headers and all.
type keptBackend struct {
	fakeBackend
	mu  sync.Mutex
	got []*http.Request
}

func (k *keptBackend) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		io.Copy(io.Discard, r.Body)
	}
	k.mu.Lock()
	k.got = append(k.got, r)
	k.mu.Unlock()
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
}

// Requests written to make the gateway and the backend read them apart:
// two lengths, two encodings, a request hidden in a body, a target with
// its own host, paths spelt another way, the gateway's own headers sent
// twice. Each gets a plain answer at the gateway, or reaches the backend
// as one clean request over the link (HTTP/2, written by the gateway: the
// backend never reads a visitor's bytes).
func TestRequestParsing(t *testing.T) {
	b := &keptBackend{fakeBackend: fakeBackend{connected: true}}
	srv := httptest.NewServer(Handler(b, "1.0", nil, nil, quiet))
	defer srv.Close()
	const host = "Host: x\r\n"
	for _, c := range []struct {
		name, req string
		want      []string // the status of each answer, in order
		reached   []string // the paths the backend got, in order
	}{
		{"two lengths", "POST /api/v1/signin HTTP/1.1\r\n" + host + "Content-Length: 5\r\nContent-Length: 6\r\n\r\nabcdef", []string{"400"}, nil},
		{"a request hidden behind two encodings", "POST /api/v1/signin HTTP/1.1\r\n" + host + "Transfer-Encoding: chunked\r\nContent-Length: 40\r\n\r\n0\r\n\r\nGET /admin/ HTTP/1.1\r\n" + host + "\r\n", []string{"200", "404"}, []string{"/api/v1/signin"}},
		{"an unknown encoding", "POST /api/v1/signin HTTP/1.1\r\n" + host + "Transfer-Encoding: xchunked\r\n\r\n", []string{"501"}, nil},
		{"two encodings in one", "POST /api/v1/signin HTTP/1.1\r\n" + host + "Transfer-Encoding: chunked, identity\r\n\r\n", []string{"501"}, nil},
		{"two encodings", "POST /api/v1/signin HTTP/1.1\r\n" + host + "Transfer-Encoding: chunked\r\nTransfer-Encoding: identity\r\n\r\n0\r\n\r\n", []string{"501"}, nil},
		{"two hosts", "GET / HTTP/1.1\r\n" + host + "Host: y\r\n\r\n", []string{"400"}, nil},
		{"a space before the colon", "GET / HTTP/1.1\r\nHost : x\r\n\r\n", []string{"400"}, nil},
		{"absolute target to the panel", "GET http://elsewhere.example/admin/ HTTP/1.1\r\n" + host + "\r\n", []string{"404"}, nil},
		{"absolute target to the link", "GET http://elsewhere.example/link/v1/events HTTP/1.1\r\n" + host + "\r\n", []string{"404"}, nil},
		{"absolute target to a page", "GET http://elsewhere.example/ HTTP/1.1\r\n" + host + "\r\n", []string{"200"}, []string{"/"}},
		{"asterisk", "GET * HTTP/1.1\r\n" + host + "\r\n", []string{"404"}, nil},
		{"CONNECT", "CONNECT elsewhere.example:443 HTTP/1.1\r\nHost: elsewhere.example:443\r\n\r\n", []string{"404"}, nil},
		{"HTTP/2 as text", "GET / HTTP/2.0\r\n" + host + "\r\n", []string{"505"}, nil},
		{"a double slash", "GET //admin/ HTTP/1.1\r\n" + host + "\r\n", []string{"404"}, nil},
		{"an encoded slash", "GET /%2fadmin/ HTTP/1.1\r\n" + host + "\r\n", []string{"404"}, nil},
		{"an encoded slash, capital", "GET /api/v1/titles/a%2Fb HTTP/1.1\r\n" + host + "\r\n", []string{"404"}, nil},
		{"a backslash", "GET /\\admin/ HTTP/1.1\r\n" + host + "\r\n", []string{"404"}, nil},
		{"an encoded backslash", "GET /%5cadmin/ HTTP/1.1\r\n" + host + "\r\n", []string{"404"}, nil},
		{"encoded dots", "GET /api/v1/%2e%2e/%2e%2e/admin/ HTTP/1.1\r\n" + host + "\r\n", []string{"404"}, nil},
		{"encoded twice", "GET /api/v1/%252e%252e/admin/ HTTP/1.1\r\n" + host + "\r\n", []string{"404"}, nil},
		{"dots and an encoded slash", "GET /api/v1/..%2f..%2fadmin/ HTTP/1.1\r\n" + host + "\r\n", []string{"404"}, nil},
		{"a dot segment on a page", "GET /title/./x HTTP/1.1\r\n" + host + "\r\n", nil, nil},
		{"a parameter on a segment", "GET /admin;x HTTP/1.1\r\n" + host + "\r\n", []string{"404"}, nil},
		{"a null", "GET /\x00admin HTTP/1.1\r\n" + host + "\r\n", []string{"400"}, nil},
		{"an encoded null", "GET /%00admin HTTP/1.1\r\n" + host + "\r\n", []string{"404"}, nil},
		{"a folded header", "GET / HTTP/1.1\r\n" + host + "X-A: a\r\n b\r\n\r\n", []string{"200"}, []string{"/"}},
		{"bare line feeds", "GET / HTTP/1.1\nHost: x\n\n", []string{"200"}, []string{"/"}},
		{"the gateway's own headers, twice", "GET / HTTP/1.1\r\n" + host + "Overfin-Client-Addr: 192.0.2.1\r\nOverfin-Client-Addr: 192.0.2.2\r\nOverfin-Check: 1\r\nOverfin-Check: 1\r\nX-Forwarded-For: 192.0.2.3\r\nX-Forwarded-For: 192.0.2.4\r\nForwarded: for=192.0.2.5\r\nX-Real-IP: 192.0.2.6\r\n\r\n", []string{"200"}, []string{"/"}},
		{"HTTP/2 asked by upgrade", "GET / HTTP/1.1\r\n" + host + "Upgrade: h2c\r\nConnection: Upgrade, HTTP2-Settings\r\nHTTP2-Settings: AAMAAABkAARAAAAAAAIAAAAA\r\n\r\n", []string{"200"}, []string{"/"}},
	} {
		b.mu.Lock()
		before := len(b.got)
		b.mu.Unlock()
		conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(time.Second))
		io.WriteString(conn, c.req)
		all, _ := io.ReadAll(conn) // until the gateway closes, or the deadline
		conn.Close()
		var got []string
		for _, line := range strings.Split(string(all), "\r\n") {
			if f := strings.Fields(line); len(f) >= 2 && strings.HasPrefix(f[0], "HTTP/") {
				got = append(got, f[1])
			}
		}
		if c.want != nil && strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("%s: answered %v, want %v", c.name, got, c.want)
		}
		b.mu.Lock()
		sent := b.got[before:]
		b.mu.Unlock()
		var paths []string
		for _, r := range sent {
			paths = append(paths, r.URL.Path)
			for _, p := range []string{"/admin", "/apps", "/link/"} {
				if strings.HasPrefix(strings.ToLower(r.URL.Path), p) {
					t.Errorf("%s reached the backend as %s", c.name, r.URL.Path)
				}
			}
			// Only the gateway names the visitor, once.
			if v := r.Header.Values("Overfin-Client-Addr"); len(v) != 1 || strings.HasPrefix(v[0], "192.0.2.") {
				t.Errorf("%s: the backend was told the visitor is %v", c.name, v)
			}
			for _, h := range []string{"Overfin-Check", "X-Forwarded-For", "Forwarded", "X-Real-Ip", "Upgrade", "Connection", "Http2-Settings", "Transfer-Encoding", "Content-Length"} {
				if v := r.Header.Values(h); len(v) > 0 {
					t.Errorf("%s: the backend got %s: %v", c.name, h, v)
				}
			}
		}
		if c.want != nil && strings.Join(paths, " ") != strings.Join(c.reached, " ") {
			t.Errorf("%s: the backend got %v, want %v", c.name, paths, c.reached)
		}
	}
}

func TestCanonicalPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/": true, "/title/m550988/l-homme-libre": true, "/api/v1/titles/peer:3:movie:tmdb:12": true, "/search": true,
		"/title/l%C3%A9a": true, "/a/": true,
		"//": false, "/a//b": false, "/./a": false, "/a/..": false, "/a/../b": false, `/a\b`: false,
		"/a%2Fb": false, "/a%5Cb": false, "/a%2fb": false, "/a\x01": false, "/a\x7f": false, "a": false,
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.URL.Path = p
		r.URL.RawPath = ""
		if strings.Contains(p, "%") {
			r.URL.RawPath = p
			r.URL.Path = strings.NewReplacer("%2F", "/", "%2f", "/", "%5C", `\`, "%C3%A9", "é").Replace(p)
		}
		if got := canonicalPath(r.URL.Path, r.URL.EscapedPath()); got != want {
			t.Errorf("%q: %v, want %v", p, got, want)
		}
	}
}
