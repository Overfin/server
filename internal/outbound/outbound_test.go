package outbound

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"testing"
)

// fakeNames answers lookups for the tests' names.
func fakeNames(t *testing.T, names map[string][]string) {
	was := lookup
	lookup = func(_ context.Context, _, host string) ([]netip.Addr, error) {
		addrs, ok := names[host]
		if !ok {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		var out []netip.Addr
		for _, a := range addrs {
			out = append(out, netip.MustParseAddr(a))
		}
		return out, nil
	}
	t.Cleanup(func() { lookup = was })
}

// dialed records what Direct dials, without dialing.
func dialed(t *testing.T) *[]string {
	var got []string
	was := connect
	connect = func(_ context.Context, _, addr string) (net.Conn, error) {
		got = append(got, addr)
		c1, c2 := net.Pipe()
		c2.Close()
		return c1, nil
	}
	t.Cleanup(func() { connect = was })
	return &got
}

// The addresses refused for peers, and those taken: LAN and public ones.
func TestRefused(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1": true, "127.1.2.3": true, "::1": true, "169.254.169.254": true, "fe80::1": true,
		"0.0.0.0": true, "::": true, "224.0.0.1": true, "ff02::1": true, "::ffff:127.0.0.1": true,
		"::ffff:169.254.169.254": true, "0.1.2.3": true, "255.255.255.255": true, "240.0.0.1": true,
		"::ffff:255.255.255.255": true,
		// Clouds' metadata services outside link-local, and IPv4 ones
		// reached through NAT64 or 6to4.
		"100.100.100.200": true, "168.63.129.16": true, "fd00:ec2::254": true,
		"64:ff9b::a9fe:a9fe": true, "64:ff9b:1::a9fe:a9fe": true, "2002:a9fe:a9fe::1": true,
		"64:ff9b::7f00:1": true, "2002:7f00:1::": true, "64:ff9b::6464:64c8": true,
		"64:ff9b::c000:201": false, "2002:c000:201::1": false, "100.64.0.1": false,
		"10.1.2.3": false, "192.168.1.5": false, "172.16.0.1": false, "fd00::1": false,
		"192.0.2.1": false, "2001:db8::1": false,
	} {
		if got := Refused(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: %v, want %v", addr, got, want)
		}
	}
}

// Direct never connects to loopback, link-local, a name that resolves
// there, or IPv4-mapped loopback: nothing is dialed at all.
func TestDirectRefuses(t *testing.T) {
	fakeNames(t, map[string][]string{
		"loopback.example.org": {"127.0.0.1"},
		"mapped.example.org":   {"::ffff:127.0.0.1"},
		"metadata.example.org": {"169.254.169.254"},
		"mixed.example.org":    {"192.0.2.10", "127.0.0.1"},
	})
	got := dialed(t)
	for _, addr := range []string{
		"127.0.0.1:443", "[::1]:443", "169.254.169.254:80", "[fe80::1%eth0]:443",
		"[::ffff:127.0.0.1]:443", "[::ffff:169.254.169.254]:80", "0.0.0.0:443", "[ff02::1]:443",
		"loopback.example.org:443", "mapped.example.org:443", "metadata.example.org:80",
		"mixed.example.org:443",
	} {
		if _, err := Direct(context.Background(), "tcp", addr); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v, want refused", addr, err)
		}
	}
	if len(*got) != 0 {
		t.Errorf("dialed %v", *got)
	}
}

// A private-range address, given or resolved, is dialed as checked.
func TestDirectPrivate(t *testing.T) {
	fakeNames(t, map[string][]string{"lan.example.org": {"10.1.2.3"}, "v6.example.org": {"fd00::5"}})
	got := dialed(t)
	for _, addr := range []string{"10.1.2.3:8443", "lan.example.org:8443", "192.168.1.5:443", "v6.example.org:443"} {
		c, err := Direct(context.Background(), "tcp", addr)
		if err != nil {
			t.Fatalf("%s: %v", addr, err)
		}
		c.Close()
	}
	want := []string{"10.1.2.3:8443", "10.1.2.3:8443", "192.168.1.5:443", "[fd00::5]:443"}
	if len(*got) != len(want) {
		t.Fatalf("dialed %v, want %v", *got, want)
	}
	for i := range want {
		if (*got)[i] != want[i] {
			t.Errorf("dialed %v, want %v", *got, want)
		}
	}
}

// The default dialer refuses a peer on this machine; the operator's
// allow_loopback lets it through, and nothing else.
func TestAllowLoopback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "same machine")
	}))
	defer ts.Close()
	client := &http.Client{Transport: Transport()}
	if res, err := client.Get(ts.URL); err == nil {
		res.Body.Close()
		t.Fatal("loopback reached by default")
	} else if !errors.Is(err, ErrRefused) {
		t.Fatalf("by default: %v, want refused", err)
	}
	AllowLoopback(true)
	defer AllowLoopback(false)
	res, err := client.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(b) != "same machine" {
		t.Errorf("got %q", b)
	}
	if !Refused(netip.MustParseAddr("169.254.169.254")) || !Refused(netip.MustParseAddr("0.0.0.0")) {
		t.Error("allow_loopback let link-local or unspecified through")
	}
}

// MSFP's destination vectors (vectors/v1/strict.json, copied as it is):
// each address refused or taken as they say.
func TestDestinationVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/v1-strict.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Destinations []struct {
			Text   string
			Valid  bool
			Reason string
		}
	}
	if err := json.Unmarshal(raw, &v); err != nil || len(v.Destinations) == 0 {
		t.Fatalf("vectors: %v", err)
	}
	for _, d := range v.Destinations {
		ip, err := netip.ParseAddr(d.Text)
		if err != nil {
			t.Errorf("%s: %v", d.Text, err)
			continue
		}
		if Refused(ip) == d.Valid {
			t.Errorf("%s: refused %v, want valid %v (%s)", d.Text, Refused(ip), d.Valid, d.Reason)
		}
	}
}
