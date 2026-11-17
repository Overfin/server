package federation

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/overfin/server/internal/msfp"
)

// What a server names of its own: the operator's pins, or the one its
// https address serves, nothing for http, and nothing when its address
// can't be reached from itself.
func TestOwnCertificates(t *testing.T) {
	ctx := context.Background()
	var none *Certificates
	if got, err := none.For(ctx, "https://a.example.org"); got != nil || err != nil {
		t.Fatalf("none: %v %v", got, err)
	}
	set := &Certificates{Pins: []string{"x"}}
	if got, _ := set.For(ctx, "https://a.example.org"); !slices.Equal(got, []string{"x"}) {
		t.Fatalf("the operator's: %v", got)
	}
	probed := &Certificates{}
	if got, err := probed.For(ctx, "http://192.0.2.10:8096"); got != nil || err != nil {
		t.Fatalf("over http: %v %v", got, err)
	}
	if got, err := probed.For(ctx, "https://127.0.0.1:1"); got != nil || err == nil {
		t.Fatalf("unreachable: %v %v", got, err)
	}
	a := newServerOn(t, "A", true)
	u, _ := url.Parse(a.url)
	cert := tlsCertOf(t, u.Host)
	if got, err := (&Certificates{}).For(ctx, a.url); err != nil || !slices.Equal(got, []string{msfp.Pin(cert)}) {
		t.Fatalf("served: %v %v", got, err)
	}
}

// tlsCertOf is the certificate host serves.
func tlsCertOf(t *testing.T, host string) *x509.Certificate {
	t.Helper()
	conn, err := tls.Dial("tcp", host, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0]
}

// Two servers whose certificates no public authority issued pair over
// HTTPS: pair and hello take any certificate, and each names its own in
// its hellos; every other call then takes only the one named. A server
// that stops naming it is no longer reached; one that names it again (a
// renewal) is, at the next call, which asks its hello again.
func TestCertificatesNamedInHello(t *testing.T) {
	ctx := context.Background()
	a, b := newServerOn(t, "A", true), newServerOn(t, "B", true)
	u, _ := url.Parse(a.url)
	pin := msfp.Pin(tlsCertOf(t, u.Host))
	code, _, _ := a.s.NewCode(ctx, "")
	aOnB, err := b.s.UseCode(ctx, code)
	if err != nil {
		t.Fatalf("pairing over HTTPS: %v", err)
	}
	if len(aOnB.Certificates) != 0 {
		t.Fatalf("named before any hello: %v", aOnB.Certificates)
	}
	// Strict before any hello: nothing named, not trusted.
	if _, err := a.s.TheirLibraries(ctx, onlyPeer(t, a)); err == nil {
		t.Fatal("libraries from B before it was accepted")
	}
	if _, err := a.s.Accept(ctx, onlyPeer(t, a).ID); err != nil {
		t.Fatal(err)
	}
	// A's hello named A's certificate to B, and B's answer B's to A.
	aOnB = onlyPeer(t, b)
	bOnA := onlyPeer(t, a)
	if aOnB.State != "paired" || !slices.Equal(aOnB.Certificates, []string{pin}) || !slices.Equal(bOnA.Certificates, []string{pin}) {
		t.Fatalf("after accepting: B's view %+v, A's view %+v", aOnB, bOnA)
	}
	if _, err := b.s.TheirLibraries(ctx, aOnB); err != nil {
		t.Fatalf("libraries over a named certificate: %v", err)
	}
	// A names none any more: B learns it at its next hello, and is
	// refused A's certificate from then on.
	a.s.Certificates = nil
	if aOnB, err = b.s.Check(ctx, aOnB); err != nil || len(aOnB.Certificates) != 0 {
		t.Fatalf("after A named none: %+v %v", aOnB, err)
	}
	if _, err := b.s.TheirLibraries(ctx, aOnB); !errors.Is(err, msfp.ErrUntrustedCertificate) {
		t.Fatalf("libraries with nothing named: %v", err)
	}
	// A names it again: B's next call asks A's hello, and goes through.
	a.s.Certificates = &Certificates{}
	if _, err := b.s.TheirLibraries(ctx, aOnB); err != nil {
		t.Fatalf("libraries after a renewal: %v", err)
	}
	if got := onlyPeer(t, b); !slices.Equal(got.Certificates, []string{pin}) {
		t.Fatalf("B's view after the renewal: %v", got.Certificates)
	}
}

// A first reading that fails (a reverse proxy starting after Overfin) is
// tried again soon, not a minute later: meanwhile, hellos name none, and
// peers drop the pins they had.
func TestOwnCertificatesFirstFailureRetriedSoon(t *testing.T) {
	ctx := context.Background()
	defer func(was time.Duration) { probeAgain = was }(probeAgain)
	probeAgain = 50 * time.Millisecond
	a := newServerOn(t, "A", true)
	u, _ := url.Parse(a.url)
	cert := tlsCertOf(t, u.Host)
	// The address first refuses connections, then serves.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	c := &Certificates{}
	if got, err := c.For(ctx, "https://"+addr); got != nil || err == nil {
		t.Fatalf("nothing served yet: %v %v", got, err)
	}
	ln, err = net.Listen("tcp", addr)
	if err != nil {
		t.Skip("the port was taken meanwhile")
	}
	defer ln.Close()
	go func() {
		for {
			in, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer in.Close()
				out, err := net.Dial("tcp", u.Host)
				if err != nil {
					return
				}
				defer out.Close()
				go io.Copy(out, in)
				io.Copy(in, out)
			}()
		}
	}()
	time.Sleep(100 * time.Millisecond)
	if got, err := c.For(ctx, "https://"+addr); err != nil || !slices.Equal(got, []string{msfp.Pin(cert)}) {
		t.Fatalf("served now: %v %v", got, err)
	}
}

// A hello's answer names the certificate served now, not one read up to a
// minute ago: a peer sends a hello when it was refused this server's
// certificate, after a renewal.
func TestOwnCertificatesFreshForHellos(t *testing.T) {
	ctx := context.Background()
	one, two := newServerOn(t, "A", true), newServerOn(t, "B", true)
	u1, _ := url.Parse(one.url)
	u2, _ := url.Parse(two.url)
	var mu sync.Mutex
	target := u1.Host
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			in, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			to := target
			mu.Unlock()
			go func() {
				defer in.Close()
				out, err := net.Dial("tcp", to)
				if err != nil {
					return
				}
				defer out.Close()
				go io.Copy(out, in)
				io.Copy(in, out)
			}()
		}
	}()
	addr := "https://" + ln.Addr().String()
	c := &Certificates{}
	first, err := c.For(ctx, addr)
	if err != nil || !slices.Equal(first, []string{msfp.Pin(tlsCertOf(t, u1.Host))}) {
		t.Fatalf("first: %v %v", first, err)
	}
	mu.Lock()
	target = u2.Host // renewed
	mu.Unlock()
	time.Sleep(probeFresh + 100*time.Millisecond)
	if got, _ := c.For(ctx, addr); !slices.Equal(got, first) {
		t.Fatalf("within the minute, For looked again: %v", got)
	}
	if got, err := c.Fresh(ctx, addr); err != nil || !slices.Equal(got, []string{msfp.Pin(tlsCertOf(t, u2.Host))}) {
		t.Fatalf("a hello's answer names %v (%v), not the renewed one", got, err)
	}
}
