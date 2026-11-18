package msfp

import (
	"crypto/rand"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"time"
)

// Config is what the host gives Open. Only Address and Store are
// required; every other field has a safe default.
type Config struct {
	// Address is this server's address, https://host[:port], without a
	// path (MSFP core section 5.2). Another than at the last start is a
	// move (SetAddress). While this server can't be called
	// (SetUncallable), it is kept for when it can again: its address is
	// then the one of the .invalid form the engine made.
	Address string
	// Store keeps the engine's records: msfp/store/jsonfile for a small
	// host, msfp/store/memory for tests, or the host's own database.
	Store Store

	// Name is the name peers show for this server, read at each pair and
	// hello (a callback, since a host's branding may change it). Default:
	// the host of Address.
	Name func() string
	// Sealer seals the keys and secrets stored. Default: the Store's own
	// (SealerProvider); Open fails when there is neither.
	Sealer Sealer

	// Prefix is where the host mounts the routes, before
	// /api/v1/federation/ and /api/v1/stream/; "" by default. A server
	// with one serves its discovery document (Discovery), or its peers
	// can't find its calls (MSFP core section 11). Segments of A-Z a-z
	// 0-9 . _ ~ - only, as "/srv/media".
	Prefix string
	// Discovery serves the discovery document at /.well-known/msfp
	// (DiscoveryHandler). Off by default: a server that serves it tells
	// anyone it federates (MSFP core sections 11 and 14).
	Discovery bool
	// Pins are this server's certificate pins, named in its hellos (MSFP
	// core section 3.2), whatever its address: nothing is read. nil, by
	// default: the engine reads them, connecting to its own address (and
	// to its former one during a move) as a peer would. A host behind a
	// proxy that serves several certificates gives them all here.
	Pins []string
	// AllowLoopback lets the default Dialer connect to peers on this
	// machine (MSFP core section 3.3). Off by default.
	AllowLoopback bool
	// PlainHTTPForTests accepts and makes http pairings, for a host's own
	// tests only (MSFP core section 3.1). It turns the switch on for the
	// whole process (wire.AllowPlainHTTPForTests), and the engine logs a
	// warning at each start with it on.
	PlainHTTPForTests bool
	// Limits are the limits on calls here (MSFP core section 8.3).
	// Default: the specification's suggested numbers; a field left zero
	// takes its default.
	Limits CallLimits
	// NewPairing is the limits a new pairing starts with. Default: 2
	// streams, 1 transcode, no bitrate cap.
	NewPairing *Limits

	// Clock is the time; the system's by default.
	Clock Clock
	// Random is the source of keys, nonces, codes and IDs;
	// crypto/rand.Reader by default. Tests may fix it; a server never
	// does.
	Random io.Reader
	// Dialer makes connections to peers; the engine's checked one by
	// default.
	Dialer Dialer
	// Notifier is told the engine's events; without one they are only
	// logged.
	Notifier Notifier
	// Logger is where the engine logs; slog.Default() by default. Nothing
	// secret is ever logged (MSFP core section 14.1).
	Logger *slog.Logger
	// ClientAddress gives the address a request comes from, as the host's
	// trusted proxies say (MSFP core section 8.3, "behind a reverse
	// proxy"). Default: the address given by WithClient, else the
	// connection's. The engine never reads X-Forwarded-For itself.
	ClientAddress func(*http.Request) netip.Addr
	// NotFound answers the requests the engine refuses without a
	// signature, and paths it doesn't serve: give the host's own handler
	// for a path that doesn't exist, so that the two can't be told apart
	// (MSFP core section 3). Default: MSFP's 404 problem document.
	NotFound http.Handler

	// Catalog, Playback and Media are the host's own libraries, its
	// playback and its media; absent, nothing is offered and nothing
	// plays here. (Used from later steps of the engine on.)
	Catalog  Catalog
	Playback Playback
	Media    Media
	// SheetEncoder re-encodes relayed preview sheets. (Later steps.)
	SheetEncoder SheetEncoder
}

// CallLimits are the limits on calls (MSFP core section 8.3). A zero
// field takes the suggested default, given beside it.
type CallLimits struct {
	// Per network address, before any signature: 50 a second, bursts of
	// 400.
	AddressRate, AddressBurst float64
	// Per peer: 20 calls a second, bursts of 200.
	PeerRate, PeerBurst float64
	// All peers together: 200 a second, bursts of 2,000.
	AllRate, AllBurst float64
	// play per peer: 30 a minute (0.5 a second), bursts of 20.
	PlayRate, PlayBurst float64
	// Requests at once: 8 per peer, 64 in all, each with a queue twice as
	// long, waited in 5 seconds at most.
	PeerAtOnce, AllAtOnce int
	QueueWait             time.Duration
}

// withDefaults is l with each zero field at its default.
func (l CallLimits) withDefaults() CallLimits {
	or := func(v *float64, d float64) {
		if *v <= 0 {
			*v = d
		}
	}
	or(&l.AddressRate, 50)
	or(&l.AddressBurst, 400)
	or(&l.PeerRate, 20)
	or(&l.PeerBurst, 200)
	or(&l.AllRate, 200)
	or(&l.AllBurst, 2000)
	or(&l.PlayRate, 0.5)
	or(&l.PlayBurst, 20)
	if l.PeerAtOnce <= 0 {
		l.PeerAtOnce = 8
	}
	if l.AllAtOnce <= 0 {
		l.AllAtOnce = 64
	}
	if l.QueueWait <= 0 {
		l.QueueWait = 5 * time.Second
	}
	return l
}

// systemClock is the system's time, in UTC.
type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

// withDefaults fills the fields the host left out.
func (c Config) withDefaults() Config {
	if c.Clock == nil {
		c.Clock = systemClock{}
	}
	if c.Random == nil {
		c.Random = rand.Reader
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.NewPairing == nil {
		c.NewPairing = &Limits{Streams: 2, Transcodes: 1}
	}
	if c.Sealer == nil {
		if p, ok := c.Store.(SealerProvider); ok {
			c.Sealer = p.Sealer()
		}
	}
	if c.Dialer == nil {
		c.Dialer = &checkedDialer{allowLoopback: c.AllowLoopback}
	}
	if c.NotFound == nil {
		c.NotFound = http.HandlerFunc(plainNotFound)
	}
	c.Limits = c.Limits.withDefaults()
	return c
}
