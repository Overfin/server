package overfin

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"

	"github.com/overfin/server/internal/api"
	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/identity"
	"github.com/overfin/server/internal/outbound"
	"github.com/overfin/server/internal/plugin"
	apiproblem "github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/msfp"
	"github.com/overfin/server/msfp/wire"
)

// Federation (ROADMAP.md milestone 6) is the MSFP engine (package msfp):
// pairing, keys, signed calls both ways, limits, catalogs, playbacks and
// their media. Overfin is its host: its records in Overfin's database
// (store.MSFP), its own libraries and titles (store.MSFPCatalog), its
// playback and media (api.PeerHost), its connections to peers
// (internal/outbound: through the gateway on the backend), and its
// events, to the logs and the site.
//
// One engine for the whole run, opened at the first build that has an
// address for it (the site's, https://host alone), or the stored record
// of a server that can't be called; a settings change gives it the new
// address (SetAddress: a move), never a new engine.

// Overfin speaks MSFP's core, catalog, playback, media and video: films
// and series. Music is never shared nor played here.
var federationParts = []string{wire.PartCatalog, wire.PartPlayback, wire.PartMedia, wire.PartVideo}

// fedHost is federation on this server, for the whole run.
type fedHost struct {
	mu     sync.Mutex
	engine atomic.Pointer[msfp.Engine]
	// address is the address given the engine last ("" for none).
	address string
	// ctx is the run's: the engine's background work stops with it.
	ctx context.Context
	// playback is the Playback in use now, for the engine's host (none
	// while it is nil).
	playback func() (api.Playback, bool)
	// titles wakes what fetches TMDB's data for new titles: peers' ones
	// read.
	titles func()
}

// runCtx is the run's context: the background's before the run set it
// (a gateway built in a test).
func (f *fedHost) runCtx() context.Context {
	if f.ctx == nil {
		return context.Background()
	}
	return f.ctx
}

// currentPlayback is the Playback in use now; false before there's one.
func (f *fedHost) currentPlayback() (api.Playback, bool) {
	if f.playback == nil {
		return api.Playback{}, false
	}
	return f.playback()
}

// current is the engine, nil until opened.
func (f *fedHost) current() *msfp.Engine {
	if f == nil {
		return nil
	}
	return f.engine.Load()
}

// federationAddress is this server's address for federation: its site's
// origin, https://host[:port] alone; "" without one (none, one with a
// path, one in plain http but for the test setting).
func federationAddress(cfg *config.Config, logger *slog.Logger) string {
	address := wire.Origin(siteURL(cfg))
	if address == "" {
		return ""
	}
	if err := wire.CheckAddress(address); err != nil {
		logger.Info("federation: off, the site's address isn't https://host alone", "address", address, "error", err)
		return ""
	}
	return address
}

// federation is the engine for cfg: opened the first time it can be,
// its address set anew at each build after (a move, when it changed).
// nil when there's none yet.
func (sh *shared) federation(cfg *config.Config) *msfp.Engine {
	f := &sh.fed
	f.mu.Lock()
	defer f.mu.Unlock()
	address := federationAddress(cfg, sh.logger)
	if eng := f.engine.Load(); eng != nil {
		eng.SetDiscovery(cfg.Federation.Discovery)
		if address != "" && address != f.address {
			// Another site address: a move (MSFP core section 13),
			// announced once this server answers there.
			if err := eng.SetAddress(f.runCtx(), address); err != nil {
				sh.logger.Error("federation: this server's new address couldn't be noted", "error", err)
			} else {
				f.address = address
			}
		}
		return eng
	}
	if address == "" {
		// A server that can't be called needs no site address: its
		// record holds the address it federates under.
		on, err := uncallableRecord(f.runCtx(), sh.db)
		if err != nil {
			sh.logger.Error("federation: off, this server's record couldn't be read", "error", err)
			return nil
		}
		if !on {
			return nil
		}
	}
	eng, err := sh.openEngine(cfg, address)
	if err != nil {
		sh.logger.Error("federation: off, the engine couldn't start", "error", err)
		return nil
	}
	f.engine.Store(eng)
	f.address = address
	go func() {
		if err := eng.Run(f.runCtx()); err != nil && !errors.Is(err, context.Canceled) {
			sh.logger.Error("federation: the engine's background work stopped", "error", err)
		}
	}()
	return eng
}

// uncallableRecord says whether this server's stored federation record
// says it can't be called.
func uncallableRecord(ctx context.Context, db *store.Store) (bool, error) {
	on := false
	err := db.MSFP().Read(ctx, func(tx msfp.Tx) error {
		r, err := tx.Server()
		on = err == nil && r != nil && r.Uncallable
		return err
	})
	return on, err
}

// openEngine opens the engine on this server.
func (sh *shared) openEngine(cfg *config.Config, address string) (*msfp.Engine, error) {
	db := sh.db
	// Its name, without branding: its site's host, even when it can't be
	// called and federates under another.
	named := cmp.Or(wire.Origin(siteURL(cfg)), address)
	name := func() string {
		if b, err := db.Branding(context.Background()); err == nil && b.On && b.Name != "" {
			return b.Name
		}
		u, err := url.Parse(named)
		if err != nil || u.Host == "" {
			return "Overfin"
		}
		return u.Host
	}
	var pins []string
	if len(cfg.Site.Certificates) > 0 {
		pins = cfg.Site.Certificates
	}
	return msfp.Open(sh.fed.runCtx(), msfp.Config{
		Address: address, Store: db.MSFP(), Name: name, Pins: pins,
		Discovery:         cfg.Federation.Discovery,
		PlainHTTPForTests: cfg.Federation.TestPlainHTTP,
		Parts:             federationParts,
		Catalog:           store.MSFPCatalog{S: db},
		Playback:          api.PeerHost{Current: sh.fed.currentPlayback},
		Media:             api.PeerHost{Current: sh.fed.currentPlayback},
		MaxBitrateKbps:    cfg.Limits.MaxBitrateKbps,
		Dialer:            outboundDialer{},
		Notifier:          &fedNotifier{sh: sh},
		Logger:            sh.logger,
		NotFound:          http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { apiproblem.Send(w, apiproblem.NotFound, "") }),
	})
}

// outboundDialer connects to peers the way the rest of Overfin reaches
// other servers (internal/outbound): from this machine, or through the
// gateway on the backend, addresses checked either way.
type outboundDialer struct{}

func (outboundDialer) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	c, err := outbound.DialContext(ctx, network, addr)
	if errors.Is(err, outbound.ErrRefused) {
		return nil, fmt.Errorf("%w: %w", msfp.ErrRefusedDestination, err)
	}
	return c, err
}

// fedNotifier hears the engine's events: each is logged; a paired
// server's titles read wake what fetches TMDB's data for new titles; a
// playback on a peer ended there stops the viewer's player here.
type fedNotifier struct{ sh *shared }

func (n *fedNotifier) Notify(ev msfp.Event) {
	sh := n.sh
	switch ev := ev.(type) {
	case msfp.CatalogReadEnded:
		if ev.Err != nil {
			sh.logger.Info("federation: a paired server's titles couldn't be read", "peer", ev.Name, "error", ev.Err)
			return
		}
		sh.logger.Info("federation: a paired server's titles were read", "peer", ev.Name, "titles", ev.Titles, "changes", ev.Changes)
		if ev.Titles > 0 && sh.fed.titles != nil {
			sh.fed.titles()
		}
		return
	case msfp.RemotePlaybackEnded:
		if pb, ok := sh.fed.currentPlayback(); ok {
			pb.RemoteEnded(ev.Playback)
		}
	case msfp.PlaybackStarted, msfp.PlaybackEnded:
		// Logged by the playback itself.
		return
	}
	sh.logger.Info("federation: "+eventName(ev), "peer", ev.PeerID(), "event", ev)
}

// eventName is an event's type, for the logs: "PeerPaired"…
func eventName(ev msfp.Event) string {
	name := fmt.Sprintf("%T", ev)
	if i := len("msfp."); len(name) > i && name[:i] == "msfp." {
		return name[i:]
	}
	return name
}

// fedRoutes mounts the engine's handlers: the calls of paired servers
// (msfp.Engine.FederationHandler), public, signed by them; the stream
// routes are api.Stream's, the engine in front of them. Its answers are
// never compressed, nor its requests read before it.
type fedRoutes struct{ engine *msfp.Engine }

func (fedRoutes) Name() string { return "federation" }

// The calls of MSFP version 1 (core, catalog and playback), each its own
// route: no catch-all.
var federationCalls = []string{
	"POST /api/v1/federation/pair",
	"POST /api/v1/federation/hello",
	"POST /api/v1/federation/rotate",
	"POST /api/v1/federation/revoke",
	"POST /api/v1/federation/libraries",
	"POST /api/v1/federation/titles",
	"POST /api/v1/federation/play",
	"POST /api/v1/federation/progress",
	"POST /api/v1/federation/stop",
}

func (f fedRoutes) Register(r *plugin.Registrar) error {
	h := withClient(f.engine.FederationHandler())
	for _, pattern := range federationCalls {
		if err := r.HandlePublic(pattern, h); err != nil {
			return err
		}
	}
	return nil
}

// withClient gives the engine the address a request comes from, as the
// trusted proxies (or the gateway) say: it counts its limits by it.
func withClient(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a := identity.ClientIP(r.Context()); a.IsValid() {
			r = msfp.WithClient(r, a)
		}
		h.ServeHTTP(w, r)
	})
}

// discoveryFirst serves the engine's discovery document at
// /.well-known/msfp while it is on (MSFP core section 11); off, that path
// is the site's, as any other.
func discoveryFirst(f *fedHost, discovery bool, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if eng := f.current(); discovery && eng != nil && r.URL.Path == wire.DiscoveryPath {
			eng.DiscoveryHandler().ServeHTTP(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}
