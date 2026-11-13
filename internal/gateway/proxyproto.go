package gateway

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// The PROXY protocol (versions 1 and 2), as a TCP proxy in front of the
// gateway sends it (HAProxy's send-proxy-v2, say): a header before the
// connection's own bytes, naming the visitor. Read only on connections from
// the proxies named (OVERFIN_PROXY_PROTOCOL_FROM); others are taken as they
// are, and a header from them would be refused by TLS or HTTP like any
// garbage.

var v2Signature = []byte("\r\n\r\n\x00\r\nQUIT\n")

// ProxyListener reads the PROXY protocol's header on connections from
// from, and gives them the visitor's address as their remote address.
type ProxyListener struct {
	net.Listener
	From []netip.Prefix
}

func (l *ProxyListener) fromProxy(a net.Addr) bool {
	ap, err := netip.ParseAddrPort(a.String())
	if err != nil {
		return false
	}
	for _, p := range l.From {
		if p.Contains(ap.Addr().Unmap()) {
			return true
		}
	}
	return false
}

// Accept waits for a connection; one from a proxy whose header is missing
// or wrong is closed and skipped.
func (l *ProxyListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if !l.fromProxy(c.RemoteAddr()) {
			return c, nil
		}
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		src, err := readProxyHeader(c)
		c.SetReadDeadline(time.Time{})
		if err != nil {
			c.Close()
			continue
		}
		if src == nil { // LOCAL, or UNKNOWN: the proxy's own connection
			return c, nil
		}
		return &proxiedConn{Conn: c, remote: src}, nil
	}
}

type proxiedConn struct {
	net.Conn
	remote net.Addr
}

func (c *proxiedConn) RemoteAddr() net.Addr { return c.remote }

// readProxyHeader reads the header exactly (nothing after it), and gives
// the visitor's address; nil when the header names none.
func readProxyHeader(r io.Reader) (net.Addr, error) {
	start := make([]byte, 6)
	if _, err := io.ReadFull(r, start); err != nil {
		return nil, err
	}
	switch {
	case string(start) == "PROXY ":
		return readV1(r)
	case bytes.Equal(start, v2Signature[:6]):
		return readV2(r)
	default:
		return nil, errors.New("no PROXY header")
	}
}

func readV1(r io.Reader) (net.Addr, error) {
	var line []byte
	one := make([]byte, 1)
	for {
		if _, err := io.ReadFull(r, one); err != nil {
			return nil, err
		}
		if one[0] == '\n' {
			break
		}
		if len(line) > 100 {
			return nil, errors.New("PROXY v1 header too long")
		}
		line = append(line, one[0])
	}
	f := strings.Fields(strings.TrimSuffix(string(line), "\r"))
	if len(f) >= 1 && f[0] == "UNKNOWN" {
		return nil, nil
	}
	if len(f) != 5 || (f[0] != "TCP4" && f[0] != "TCP6") {
		return nil, errors.New("PROXY v1 header not understood")
	}
	ip, err := netip.ParseAddr(f[1])
	if err != nil {
		return nil, err
	}
	port, err := strconv.ParseUint(f[3], 10, 16)
	if err != nil {
		return nil, err
	}
	return net.TCPAddrFromAddrPort(netip.AddrPortFrom(ip, uint16(port))), nil
}

func readV2(r io.Reader) (net.Addr, error) {
	rest := make([]byte, 10) // the signature's last 6, version and command, family, length
	if _, err := io.ReadFull(r, rest); err != nil {
		return nil, err
	}
	if !bytes.Equal(rest[:6], v2Signature[6:]) || rest[6]>>4 != 2 {
		return nil, errors.New("not a PROXY v2 header")
	}
	n := binary.BigEndian.Uint16(rest[8:10])
	if n > 1024 {
		return nil, errors.New("PROXY v2 header too long")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	if rest[6]&0x0f == 0 { // LOCAL
		return nil, nil
	}
	switch rest[7] {
	case 0x11: // TCP over IPv4
		if len(body) < 12 {
			return nil, errors.New("PROXY v2 header too short")
		}
		ip := netip.AddrFrom4([4]byte(body[0:4]))
		return net.TCPAddrFromAddrPort(netip.AddrPortFrom(ip, binary.BigEndian.Uint16(body[8:10]))), nil
	case 0x21: // TCP over IPv6
		if len(body) < 36 {
			return nil, errors.New("PROXY v2 header too short")
		}
		ip := netip.AddrFrom16([16]byte(body[0:16]))
		return net.TCPAddrFromAddrPort(netip.AddrPortFrom(ip, binary.BigEndian.Uint16(body[32:34]))), nil
	default:
		return nil, nil // another family: the proxy's own address stays
	}
}
