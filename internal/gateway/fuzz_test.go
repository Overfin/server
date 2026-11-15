package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/netip"
	"testing"
	"time"
)

// A PROXY header is read exactly: on success, nothing past it (the
// visitor's TLS follows); an address given is a plain one of the family
// the header names. Anything else is refused, without a panic.
func FuzzProxyHeader(f *testing.F) {
	f.Add([]byte("PROXY TCP4 192.0.2.1 198.51.100.2 56324 443\r\nTLS"))
	f.Add([]byte("PROXY TCP6 2001:db8::1 2001:db8::2 56324 443\r\n"))
	f.Add([]byte("PROXY UNKNOWN\r\n"))
	f.Add([]byte("PROXY TCP4 2001:db8::1 192.0.2.2 1 2\r\n"))
	f.Add([]byte("PROXY TCP6 fe80::1%eth0 fe80::2 1 2\r\n"))
	f.Add([]byte("PROXY TCP6 ::ffff:192.0.2.1 ::ffff:192.0.2.2 1 2\r\n"))
	f.Add([]byte("PROXY TCP6 192.0.2.1 192.0.2.2 1 2\r\n"))
	f.Add([]byte("PROXY TCP4 192.0.2.1 192.0.2.2 99999 443\r\n"))
	f.Add([]byte("PROXY " + string(bytes.Repeat([]byte("A"), 200)) + "\n"))
	v2 := func(cmd, fam byte, body []byte) []byte {
		h := append([]byte{}, v2Signature[:]...)
		h = append(h, cmd, fam, 0, 0)
		binary.BigEndian.PutUint16(h[14:16], uint16(len(body)))
		return append(h, body...)
	}
	f.Add(v2(0x21, 0x11, []byte{192, 0, 2, 1, 198, 51, 100, 2, 0xdc, 0x04, 0x01, 0xbb}))
	f.Add(v2(0x21, 0x21, make([]byte, 36)))
	f.Add(v2(0x20, 0x00, nil))
	f.Add(v2(0x21, 0x11, []byte{1, 2, 3}))
	f.Add(v2(0x21, 0x31, make([]byte, 216)))
	f.Add(v2(0x11, 0x11, make([]byte, 12)))
	f.Fuzz(func(t *testing.T, data []byte) {
		r := bytes.NewReader(data)
		addr, err := readProxyHeader(r)
		if err != nil {
			return
		}
		read := len(data) - r.Len()
		v1 := bytes.HasPrefix(data, []byte("PROXY "))
		if v1 {
			if read != bytes.IndexByte(data, '\n')+1 {
				t.Fatalf("v1: read %d bytes of %q", read, data)
			}
		} else if want := 16 + int(binary.BigEndian.Uint16(data[14:16])); read != want {
			t.Fatalf("v2: read %d bytes, the header has %d", read, want)
		}
		if addr == nil {
			return
		}
		a, ok := addr.(*net.TCPAddr)
		if !ok || a.Zone != "" || (a.IP.To4() == nil && len(a.IP) != net.IPv6len) {
			t.Fatalf("%q: address %#v", data, addr)
		}
		// TCP4 gives IPv4; TCP6 gives IPv6, or IPv4 when it wrote an
		// IPv4 address as IPv6, never one written as IPv4.
		if fields := bytes.Fields(data[:bytes.IndexByte(data, '\n')]); v1 && len(fields) > 2 {
			src, _ := netip.ParseAddr(string(fields[2]))
			if string(fields[1]) == "TCP4" && a.IP.To4() == nil || string(fields[1]) == "TCP6" && src.Is4() {
				t.Fatalf("%q: address %v of the other family", data, a)
			}
		}
	})
}

// One line of the backend's events: a line that isn't one changes
// nothing; a revocation is refused at once, and counts from its number;
// a reset starts the count again; nothing panics.
func FuzzEvents(f *testing.F) {
	f.Add([]byte(`{"type":"revoked","seq":12,"hash":"00","expires":1}`), "login", int64(3))
	f.Add([]byte(`{"type":"reset"}`), "", int64(0))
	f.Add([]byte(`{"type":"new","images":["/api/v1/images/x",null,""]}`), "a", int64(-1))
	f.Add([]byte(`{"type":"access"}`), "b", int64(1<<62))
	f.Add([]byte(`{"type":"ping"}`), "c", int64(5))
	f.Add([]byte(`{"type":"revoked","seq":"12"}`), "d", int64(5))
	f.Add([]byte(`{"type":"revoked","seq":-5,"hash":"`+hex.EncodeToString(make([]byte, 32))+`","expires":-1}`), "e", int64(5))
	f.Add([]byte(`not json`), "f", int64(5))
	f.Add([]byte(`{"type":"revoked","type":"reset"}`), "g", int64(5))
	f.Fuzz(func(t *testing.T, line []byte, login string, seq int64) {
		var news, access int
		rv := &Revocations{New: func([]string) { news++ }, Access: func() { access++ }, seq: 7}
		var e map[string]any
		if json.Unmarshal(line, &e) != nil {
			rv.event(line)
			if rv.seq != 7 || len(rv.revoked) != 0 || news+access != 0 {
				t.Fatalf("%q isn't an event, yet it changed something", line)
			}
		} else {
			rv.event(line)
			if rv.seq < 0 {
				t.Fatalf("%q: counting from %d", line, rv.seq)
			}
		}
		// A revocation, as the backend writes it, after whatever came.
		before := rv.seq
		sum := sha256.Sum256([]byte(login))
		ev, _ := json.Marshal(map[string]any{"type": "revoked", "seq": seq, "hash": hex.EncodeToString(sum[:]), "expires": time.Now().Add(time.Hour).Unix()})
		rv.event(ev)
		if !rv.Revoked(login) {
			t.Fatalf("after %q, a revocation of %q wasn't kept", line, login)
		}
		if rv.seq != max(before, seq) {
			t.Fatalf("counting from %d, want %d", rv.seq, max(before, seq))
		}
		rv.event([]byte(`{"type":"reset"}`))
		if rv.seq != 0 || !rv.Revoked(login) {
			t.Fatalf("a reset: counting from %d, revoked kept %v", rv.seq, rv.Revoked(login))
		}
	})
}
