package msfp

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/overfin/server/msfp/wire"
)

// This server's own certificate pins (MSFP core section 3.2), named in
// its hellos: Config.Pins when the host gives them, read from nothing;
// otherwise read by connecting to its own address as a peer would, with
// SNI set to its host name, and naming the pin of the leaf certificate
// served there. During a move, those of its former address too, so that
// a peer still calling it there keeps reaching it.

// Times of readings.
const (
	// pinsFresh is how old a reading may be for a hello answer (a peer
	// asking may just have been refused the certificate before a renewal);
	// pinsSending, for a hello this server sends.
	pinsFresh   = 2 * time.Second
	pinsSending = time.Minute
	// After a failed reading the next waits pinsRetry, or pinsFirstRetry
	// while no reading has ever worked.
	pinsRetry      = time.Minute
	pinsFirstRetry = 5 * time.Second
)

// ownPins are the readings of this server's certificates, by address.
type ownPins struct {
	mu       sync.Mutex
	readings map[string]*pinReading
}

// pinReading is the last good reading at an address, and the last
// attempt; mu makes the readings at one address one at a time.
type pinReading struct {
	mu     sync.Mutex
	pins   []string
	good   time.Time // when pins were read
	failed time.Time // the last failed attempt, after good
}

// ownPins gives the pins this server names in a hello it sends, or
// answers (answering).
func (e *Engine) ownPins(ctx context.Context, answering bool) []string {
	if e.cfg.Pins != nil {
		return wire.CleanPins(e.cfg.Pins)
	}
	a := e.address()
	addresses := []string{a.address}
	if e.formerHost() != "" {
		addresses = append(addresses, a.former)
	}
	maxAge := pinsSending
	if answering {
		maxAge = pinsFresh
	}
	var pins []string
	for _, address := range addresses {
		if !wire.Uncallable(address) {
			pins = append(pins, e.pinsAt(ctx, address, maxAge)...)
		}
	}
	return wire.CleanPins(pins)
}

// pinsAt is the pins served at address, read again when the last reading
// is older than maxAge; a failed reading keeps the last good one.
func (e *Engine) pinsAt(ctx context.Context, address string, maxAge time.Duration) []string {
	e.pins.mu.Lock()
	if e.pins.readings == nil {
		e.pins.readings = map[string]*pinReading{}
	}
	r := e.pins.readings[address]
	if r == nil {
		r = &pinReading{}
		e.pins.readings[address] = r
	}
	e.pins.mu.Unlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	now := e.now()
	retry := pinsRetry
	if r.good.IsZero() {
		retry = pinsFirstRetry
	}
	switch {
	case !r.good.IsZero() && now.Sub(r.good) < maxAge:
		return r.pins
	case !r.failed.IsZero() && now.Sub(r.failed) < retry:
		return r.pins
	}
	pin, err := e.readPin(ctx, address)
	if err != nil {
		r.failed = now
		e.log.Info("msfp: this server's certificate couldn't be read at its address", "address", address, "error", err)
		return r.pins
	}
	r.pins, r.good, r.failed = []string{pin}, now, time.Time{}
	return r.pins
}

// readPin connects to address as a peer would (through the Dialer),
// with SNI set to its host name (none for an IP address), and gives the
// pin of the leaf certificate served there.
func (e *Engine) readPin(ctx context.Context, address string) (string, error) {
	u, err := url.Parse(address)
	if err != nil {
		return "", err
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := e.cfg.Dialer.Dial(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return "", err
	}
	defer conn.Close()
	cfg := &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2", "http/1.1"}}
	if net.ParseIP(u.Hostname()) == nil {
		cfg.ServerName = u.Hostname()
	}
	tc := tls.Client(conn, cfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		return "", err
	}
	certs := tc.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", errors.New("no certificate")
	}
	return wire.Pin(certs[0]), nil
}
