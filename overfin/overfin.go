// Package overfin is Overfin: the server between its apps and Jellyfin,
// with every feature built in. The official build (cmd/overfin) runs it
// with overfin-web, Overfin's own web client, as the site it serves.
package overfin

import (
	"cmp"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/msfp-io/msfp-go/describe"
	"github.com/msfp-io/msfp-go/wire"
	"github.com/overfin/server/internal/admin"
	"github.com/overfin/server/internal/api"
	"github.com/overfin/server/internal/backend"
	"github.com/overfin/server/internal/catalog"
	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/connector"
	"github.com/overfin/server/internal/cookie"
	"github.com/overfin/server/internal/copies"
	"github.com/overfin/server/internal/delivery"
	"github.com/overfin/server/internal/emails"
	"github.com/overfin/server/internal/files"
	"github.com/overfin/server/internal/gpu"
	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/imdb"
	"github.com/overfin/server/internal/introskipper"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/keys"
	"github.com/overfin/server/internal/link"
	"github.com/overfin/server/internal/notices"
	"github.com/overfin/server/internal/oidc"
	"github.com/overfin/server/internal/outbound"
	"github.com/overfin/server/internal/personal"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/previews"
	"github.com/overfin/server/internal/proof"
	"github.com/overfin/server/internal/provision"
	"github.com/overfin/server/internal/ratings"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/seerr"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
	"github.com/overfin/server/internal/watchlist"
	"golang.org/x/crypto/acme/autocert"
)

// builtinSite is the web client this build includes, if any.
var builtinSite fs.FS

// Version is set at build time with
// -ldflags "-X github.com/overfin/server/overfin.Version=…" (Dockerfile).
var Version = "dev"

// Run runs Overfin, serving site as its web client when the configuration
// leaves [site] serve at "builtin" (nil for none), and exits when it stops.
// Command-line flags are parsed here.
func Run(site fs.FS) {
	builtinSite = site
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); errors.Is(err, errCheckFailed) {
		os.Exit(1) // what's wrong is already said, in plain words
	} else if err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// backendMode is set by RunBackend: Overfin runs as the backend (see
// run).
var backendMode bool

// RunBackend runs Overfin as its backend, in the overfin-backend image:
// with its Jellyfin beside it, serving the site to its gateway over the
// link, the control panel on its own listener for private addresses only.
func RunBackend(site fs.FS) {
	backendMode = true
	Run(site)
}

// pairBackend starts pairing the backend with its gateway, and prints the
// code to give the gateway.
func pairBackend(dir, address, serverName string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := link.NewBackend(dir, Version, http.NotFoundHandler(), slog.New(slog.DiscardHandler))
	if err != nil {
		return err
	}
	code, err := b.Pair(address, serverName)
	if err != nil {
		return err
	}
	fmt.Printf("Pairing code for the gateway at %s:\n\n  %s\n\nSet it on the gateway as OVERFIN_PAIRING and start it again; this backend pairs at its next attempt.\n", address, code)
	return nil
}

// unpairBackend forgets the remote gateway.
func unpairBackend(dir string) error {
	b, err := link.NewBackend(dir, Version, http.NotFoundHandler(), slog.New(slog.DiscardHandler))
	if err != nil {
		return err
	}
	addr, err := b.Unpair()
	if err != nil {
		return err
	}
	if addr == "" {
		fmt.Println("No remote gateway was paired.")
		return nil
	}
	fmt.Printf("The gateway at %s is forgotten; the running backend stops reaching it at its next attempt.\n", addr)
	return nil
}

// oldLocalGateway is where the one-machine compose file of earlier
// versions had the backend reach the gateway beside it, over the link.
const oldLocalGateway = "gateway:8444"

// dropOldLocalPairing forgets that pairing once the gateway beside the
// backend is on the socket (OVERFIN_LOCAL_SOCKET, without
// OVERFIN_GATEWAY_ADDRESS): that gateway no longer listens for the link,
// and the backend would otherwise keep trying it, its federation sent
// there in vain.
func dropOldLocalPairing(lb *link.Backend, logger *slog.Logger) error {
	if os.Getenv("OVERFIN_LOCAL_SOCKET") == "" || os.Getenv("OVERFIN_GATEWAY_ADDRESS") != "" {
		return nil
	}
	if addr, err := lb.Address(); err != nil || addr != oldLocalGateway {
		return err
	}
	if _, err := lb.Unpair(); err != nil {
		return err
	}
	logger.Info("link: the gateway beside the backend is now on the socket: its former pairing is forgotten", "address", oldLocalGateway)
	return nil
}

// autoPair pairs the backend with the gateway beside it, on one machine
// (the one-machine compose file): OVERFIN_GATEWAY_ADDRESS names the gateway,
// OVERFIN_PAIRING_FILE a file on a volume only the two share. Unpaired and
// with no code waiting, the backend makes one and writes it there; the
// gateway reads it; once they're paired, the file goes.
func autoPair(lb *link.Backend, logger *slog.Logger) error {
	addr, file := os.Getenv("OVERFIN_GATEWAY_ADDRESS"), os.Getenv("OVERFIN_PAIRING_FILE")
	if addr == "" || file == "" {
		return nil
	}
	lb.OnPaired = func() {
		if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			logger.Warn("link: the shared pairing code couldn't be deleted", "err", err)
		}
	}
	paired, pending, err := lb.State()
	if err != nil || paired || pending {
		return err
	}
	code, err := lb.Pair(addr, os.Getenv("OVERFIN_GATEWAY_SERVER_NAME"))
	if err != nil {
		return err
	}
	logger.Info("link: pairing with the gateway beside it, through the shared volume", "address", addr)
	return os.WriteFile(file, []byte(code+"\n"), 0o600)
}

// pairingCode is the code the operator gave the gateway to pair with its
// backend: OVERFIN_PAIRING, or the file OVERFIN_PAIRING_FILE names (the
// one-machine compose file's shared volume, written by the backend).
func pairingCode() string {
	if c := strings.TrimSpace(os.Getenv("OVERFIN_PAIRING")); c != "" {
		return c
	}
	if f := os.Getenv("OVERFIN_PAIRING_FILE"); f != "" {
		if b, err := os.ReadFile(f); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}

// firstKey generates the secret key when none was given and none was
// generated yet, for a database that doesn't exist yet only. An existing
// database without its key is a lost key, not a first start: a new key
// couldn't read it, so Overfin stops, unless it's recovering from exactly
// that (-recover-tokens).
func firstKey(cfg *config.Config, recovering bool, logger *slog.Logger) error {
	_, err := os.Stat(cfg.Storage.Path)
	if !errors.Is(err, fs.ErrNotExist) && !recovering {
		return fmt.Errorf("no secret key: OVERFIN_SECRET_KEY isn't set and %s doesn't exist, but the database "+
			"does. Restore the key from your backup; if it's lost for good, run overfin -recover-tokens once "+
			"to make a new one (profiles keep their history; PINs and logins are reset)", config.KeyFile)
	}
	key, err := config.GenerateKey()
	if err != nil {
		return err
	}
	cfg.Secrets.Key = key
	logger.Warn("generated a new secret key: back it up, apart from the database", "file", config.KeyFile)
	return nil
}

func run(logger *slog.Logger) error {
	// Everything Overfin creates (database, cache) is readable by its own
	// user only.
	syscall.Umask(0o077)

	defaultPath := os.Getenv("OVERFIN_CONFIG")
	if defaultPath == "" {
		defaultPath = "/etc/overfin/overfin.toml"
	}
	configPath := flag.String("config", defaultPath, "path to the configuration file")
	recoverTokens := flag.Bool("recover-tokens", false,
		"after the secret key was lost: give every profile a fresh Jellyfin token sealed with the current key, then exit")
	importFlag := flag.Bool("import-people", false,
		"before moving to managed sign-in: let everyone the identity provider ([connector]) has sign in with their email, keeping their accounts, then exit")
	passwordFor := flag.String("set-password", "",
		"set a new password for this email (managed sign-in), read from the terminal, then exit; for an admin locked out")
	check := flag.Bool("check", false,
		"test the configuration, the secret key, Jellyfin and the identity provider, say what's wrong, then exit; changes nothing")
	importProbesFrom := flag.String("import-probes", "",
		"import a header read (JSON lines, gzipped or not) into the copy index, beside the running Overfin, then exit; run it again to resume")
	importRoot := flag.String("import-root", "/media", "with -import-probes: the folder the header read's paths are relative to")
	var pair, serverName *string
	unpair := new(bool)
	panelPassword := new(bool)
	if backendMode {
		panelPassword = flag.Bool("set-panel-password", false,
			"make or change the control panel's own login (a username and a password, read from the terminal), then exit")
		pair = flag.String("pair", "", "pair with the gateway at this host:port: prints a one-time code to set on the gateway (OVERFIN_PAIRING), then exits")
		serverName = flag.String("server-name", "", "with -pair: the name sent to the gateway in TLS, when a proxy in front of it routes on names (default: the address's host)")
		unpair = flag.Bool("unpair", false, "forget the remote gateway (the one paired with -pair), then exit; the gateway beside the backend stays")
	}
	flag.Parse()

	// The backend: its Jellyfin is in the same container, reached on
	// 127.0.0.1 (internal/backend).
	getenv := os.Getenv
	jf := backend.FromEnv(os.Getenv)
	if backendMode {
		getenv = func(k string) string {
			if v := os.Getenv(k); v != "" || k != "OVERFIN_JELLYFIN_URL" {
				return v
			}
			return jf.LocalURL()
		}
	}

	// Without a file at the default place, the defaults: a start needs no
	// configuration at all. A file named on purpose must exist.
	if *configPath == defaultPath && !fileExists(defaultPath) {
		*configPath = ""
	}
	if *check {
		return checkSetup(*configPath, getenv, os.Stdout)
	}
	cfg, err := config.Load(*configPath, getenv)
	if err != nil {
		return err
	}
	if backendMode {
		if err := backendIdentity(cfg); err != nil {
			return err
		}
	}
	// Addresses paired servers give: this machine's own only when the
	// operator allows it (SECURITY.md rule 136). On a gateway, its egress
	// for the backend follows the same.
	outbound.AllowLoopback(cfg.Federation.AllowLoopback)
	// Plain http between servers: a test setting outside MSFP, which is
	// HTTPS only, never for a server in use.
	wire.AllowPlainHTTPForTests(cfg.Federation.TestPlainHTTP)
	if cfg.Federation.TestPlainHTTP {
		logger.Warn("federation: test_plain_http is on: plain http pairings are accepted, for testing only; turn it off on a server in use")
	}
	addr := cfg.Listen
	linkDir := filepath.Join(filepath.Dir(cfg.Storage.Path), "link")
	if backendMode {
		// The panel and the setup wizard are on the backend's own
		// listener, for private addresses only, never on the site.
		if cfg.Admin.Listen == "" {
			cfg.Admin.Listen = ":8081"
		}
		if *pair != "" {
			return pairBackend(linkDir, *pair, *serverName)
		}
		if *unpair {
			return unpairBackend(linkDir)
		}
	}
	if cfg.Secrets.Key == nil {
		if err := firstKey(cfg, *recoverTokens, logger); err != nil {
			return err
		}
	}

	k, err := keys.Derive(cfg.Secrets.Key)
	if err != nil {
		return err
	}
	box, err := seal.New(k.TokenEncryption)
	if err != nil {
		return err
	}
	db, err := store.Open(context.Background(), cfg.Storage.Path, box)
	if err != nil {
		return err
	}
	defer db.Close()
	logger.Info("database ready", "path", cfg.Storage.Path)
	// Requests are gone: those still open become wishes, once.
	if err := api.CopyRequests(context.Background(), db); err != nil {
		return fmt.Errorf("copying requests onto wishlists: %w", err)
	}
	if wide := cfg.Identity.WideProxies(); cfg.Identity.Mode == "forwardauth" && len(wide) > 0 {
		logger.Warn("identity.trusted_proxies is very wide: anyone in it may say who they are; give your reverse proxy's own address",
			"ranges", strings.Join(wide, ", "))
	}
	// Titles' ages from their ratings (the table of ages comes with
	// Overfin), and kids profiles' ceilings from before ages, as ages.
	if err := db.RefreshTitleAges(context.Background()); err != nil {
		return err
	}
	if n, err := db.AgesFromCeilings(context.Background()); err != nil {
		return err
	} else if n > 0 {
		logger.Info("kids profiles' rating ceilings became ages", "profiles", n)
	}
	// Branding files kept as they were uploaded, made Overfin's like every
	// image.
	if err := admin.PrepareBranding(context.Background(), db, logger); err != nil {
		return err
	}
	moduleDBs, err := openModules(context.Background(), db, cfg.Storage.Path, logger)
	if err != nil {
		return err
	}
	defer closeModules(moduleDBs)
	if !*recoverTokens {
		if err := db.CheckKey(context.Background(), k.Check); errors.Is(err, store.ErrWrongKey) {
			return fmt.Errorf("%w. Restore the original key; if it's lost for good, run overfin -recover-tokens once "+
				"with the new key (profiles keep their history; PINs and logins are reset)", err)
		} else if err != nil {
			return err
		}
	}

	if *recoverTokens {
		return runRecovery(cfg, db, moduleDBs, k, logger)
	}
	if *importFlag {
		return importPeople(context.Background(), cfg, db, os.Stdout)
	}
	if *passwordFor != "" {
		return setPassword(context.Background(), cfg, db, *passwordFor, os.Stdin, os.Stdout)
	}
	if *importProbesFrom != "" {
		return importProbes(cfg, db, k, *importProbesFrom, *importRoot, os.Stdout, logger)
	}
	if *panelPassword {
		return setPanelPassword(context.Background(), db, os.Stdin, os.Stdout)
	}

	// Stop cleanly on Ctrl+C or `docker stop`; reload the configuration on
	// SIGHUP (`docker kill -s HUP …`).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)

	// The listeners start now, before Overfin is ready, so the setup page can
	// come first: until then, the site says it's being set up.
	site, panel := newSwap(settingUp), newSwap(http.NotFoundHandler())
	var siteHandler http.Handler = site
	if strings.HasPrefix(cfg.Site.URL, "https://") {
		// Its public address says HTTPS: cookies are HTTPS ones, whatever
		// the proxy in front says, and browsers are told to come back over
		// HTTPS only (they heed that only when it came over HTTPS).
		siteHandler = hsts(cookie.AlwaysSecure(site))
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           siteHandler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: video streams can legitimately run for hours.
	}
	errc := make(chan error, 4)
	linkCtx, stopLink := context.WithCancel(context.Background())
	defer stopLink()
	var tlsSrv *http.Server
	var gwLink *link.Gateway
	var jellyfinDone chan struct{}
	if backendMode {
		// The backend: the site is served to its gateway over the link
		// (internal/link), never on a listener of its own; its Jellyfin
		// runs beside it, with Overfin's plugin, on 127.0.0.1 only.
		lb, err := link.NewBackend(linkDir, Version, siteHandler, logger)
		if err != nil {
			return err
		}
		if err := autoPair(lb, logger); err != nil {
			return err
		}
		if err := dropOldLocalPairing(lb, logger); err != nil {
			return err
		}
		edgeSigner = lb.SignEdge
		lb.Handle("GET "+eventsPath, eventsHandler(db))
		lb.Handle("GET /link/v1/artwork/{id}", http.HandlerFunc(prefetchHandler))
		// Paired servers are reached through the remote gateway, when
		// there's one: peers then see it, never this machine
		// (internal/outbound). Without one, straight from here: the
		// gateway beside it is on this machine anyway.
		outbound.Through(func(ctx context.Context, network, addr string) (net.Conn, error) {
			if paired, _, err := lb.State(); err == nil && paired {
				return lb.DialEgress(ctx, network, addr)
			}
			return outbound.Direct(ctx, network, addr)
		})
		// The gateway beside it (every backend has one): the site on a
		// socket in the volume they share, plain http for the LAN, so
		// without HTTPS's cookies (internal/gateway, Socket).
		if path := os.Getenv("OVERFIN_LOCAL_SOCKET"); path != "" {
			ln, err := listenSocket(path)
			if err != nil {
				return err
			}
			local := &http.Server{Handler: link.Local(Version, site), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
			go func() {
				logger.Info("serving the gateway beside it", "socket", path)
				errc <- local.Serve(ln)
			}()
			defer local.Close()
		} else {
			logger.Warn("no gateway beside the backend (OVERFIN_LOCAL_SOCKET): the LAN has no site while the internet is down")
		}
		version, err := jf.InstallPlugin()
		if err != nil {
			return err
		}
		logger.Info("Overfin's plugin is in place for Jellyfin", "version", version)
		if err := jf.LoopbackOnly(); err != nil {
			return err
		}
		jfCtx, stopJellyfin := context.WithCancel(context.Background())
		jellyfinDone = make(chan struct{})
		go func() {
			defer close(jellyfinDone)
			jf.Supervise(jfCtx, logger)
		}()
		// Jellyfin stops after Overfin, cleanly.
		defer func() {
			stopJellyfin()
			<-jellyfinDone
		}()
		go lb.Run(linkCtx)
		// Images from outside (TMDB, Jellyfin, uploads) are decoded in
		// worker processes of the backend's own, with memory and time
		// limits: a picture made to harm takes a worker down, not Overfin.
		pool, err := images.Workers(images.DecodeSlots, logger)
		if err != nil {
			return err
		}
		images.SetTransformer(pool.Transform)
		defer pool.Close()
	} else {
		// The link: the backend connects out to this gateway and answers its
		// requests (internal/link). It shares the HTTPS listener below, told
		// apart by its ALPN name, and/or takes a listener of its own
		// (OVERFIN_LINK_LISTEN), for a gateway whose HTTPS a proxy handles.
		gwLink, err = link.NewGateway(linkDir, Version, pairingCode, logger)
		if err != nil {
			return err
		}
		if a := os.Getenv("OVERFIN_LINK_LISTEN"); a != "" {
			go func() {
				logger.Info("listening for the backend", "addr", a)
				errc <- gwLink.Listen(linkCtx, a)
			}()
		}
		// HTTPS by Overfin itself: the site on its own listener, with a
		// certificate from Let's Encrypt; the main one answers Let's Encrypt's
		// checks and sends everyone else to HTTPS.
		if h := cfg.HTTPS; h.Enabled() {
			certs := &autocert.Manager{Prompt: autocert.AcceptTOS, HostPolicy: autocert.HostWhitelist(h.Domain),
				Cache: autocert.DirCache(filepath.Join(filepath.Dir(cfg.Storage.Path), "certificates")), Email: h.Email}
			tlsSrv = &http.Server{Addr: h.Listen, Handler: hsts(siteHandler), TLSConfig: certs.TLSConfig(),
				ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
			tlsSrv.TLSConfig.MinVersion = tls.VersionTLS12
			// Failed handshakes, Let's Encrypt's refusals among them, in the
			// log like everything else.
			tlsSrv.ErrorLog = slog.NewLogLogger(logger.Handler(), slog.LevelWarn)
			gwLink.Share(tlsSrv)
			srv.Handler = certs.HTTPHandler(nil)
			go func() {
				logger.Info("listening, HTTPS", "addr", h.Listen, "domain", h.Domain)
				errc <- tlsSrv.ListenAndServeTLS("", "")
			}()
		}
		go func() {
			logger.Info("listening", "addr", addr, "https", tlsSrv != nil)
			errc <- srv.ListenAndServe()
		}()

	}
	// The control panel, on its own listener (rules 82 and 129).
	var adminSrv *http.Server
	if a := cfg.Admin; a.Enabled() {
		var h http.Handler = panel
		if backendMode {
			h = admin.PrivateOnly(panel) // never the internet (rule 129)
		}
		adminSrv = &http.Server{Addr: a.Listen, Handler: h,
			ReadHeaderTimeout: 10 * time.Second, WriteTimeout: time.Minute, IdleTimeout: 2 * time.Minute}
		go func() {
			logger.Info("control panel listening", "addr", a.Listen, "https", a.TLSCert != "")
			if a.TLSCert != "" {
				errc <- adminSrv.ListenAndServeTLS(a.TLSCert, a.TLSKey)
			} else {
				errc <- adminSrv.ListenAndServe()
			}
		}()
	}
	shutdown := func() error {
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if adminSrv != nil {
			_ = adminSrv.Shutdown(shutdownCtx)
		}
		stopLink()
		if gwLink != nil {
			gwLink.Close()
		}
		if tlsSrv != nil {
			_ = tlsSrv.Shutdown(shutdownCtx)
		}
		if backendMode {
			return nil
		}
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}

	// Jellyfin: Overfin's own (rule 110), at the address the configuration
	// sets or the setup page saved. A fresh one is set up at once; any
	// other waits to be handed over on the setup page, which with managed
	// sign-in asks for the first admin too.
	if backendMode {
		// Its own Jellyfin, just started beside it: ready first.
		if err := backend.WaitHealthy(ctx, cfg.Jellyfin.URL, logger); err != nil {
			return shutdown()
		}
	}
	jellyfinSaved := cfg.Jellyfin.URL == ""
	url, key, haveJellyfin, err := ownJellyfin(ctx, cfg, db, logger)
	if err != nil {
		return err
	}
	fixedURL := cfg.Jellyfin.URL
	cfg.Jellyfin.URL, cfg.Secrets.JellyfinAdminKey = url, key
	// The Jellyfin settings Overfin needs are its own (rule 113): set back
	// on every start, and as soon as Overfin has its Jellyfin, so the
	// setup wizard's steps (Intro Skipper restarts Jellyfin) run with them.
	if haveJellyfin {
		ownSettings(ctx, db, url, key, logger)
	}
	managed := cfg.Identity.Mode == "managed"
	haveAdmin := true
	if managed {
		if haveAdmin, err = db.HasAdmin(ctx); err != nil {
			return err
		}
	}
	// On the backend, the control panel has its own login, made by the
	// setup wizard (or -set-panel-password).
	havePanelLogin := true
	if backendMode {
		if _, _, havePanelLogin, err = db.PanelAccount(ctx); err != nil {
			return err
		}
	}
	// Intro Skipper's installs: on the Jellyfin the setup wizard has, then
	// on the gateway's.
	skipper := &introskipper.Installer{Logger: logger, Jellyfin: func() (*jellyfin.Client, jellyfin.Auth, bool) {
		if cfg.Jellyfin.URL == "" || cfg.Secrets.JellyfinAdminKey == "" {
			return nil, jellyfin.Auth{}, false
		}
		jf, err := jellyfin.New(cfg.Jellyfin.URL, Version)
		return jf, jellyfin.Auth{Token: cfg.Secrets.JellyfinAdminKey, DeviceID: "overfin-admin"}, err == nil
	}}
	if !haveJellyfin || !haveAdmin {
		if !managed && adminSrv == nil {
			return errors.New("no Jellyfin of Overfin's own yet: switch on the control panel ([admin]), " +
				"and set it up or hand it over on its setup page")
		}
		done := make(chan struct{})
		setup := &admin.Setup{Code: admin.NewSetupCode(), Logger: logger, Done: func() { close(done) }}
		if backendMode {
			// Private addresses only (the panel's listener): no code.
			setup.Code, setup.NoCode = "", true
			if !havePanelLogin {
				setup.PanelLogin = db.SetPanelAccount
			}
		}
		if !haveJellyfin {
			setup.FixedURL = fixedURL
			setup.Jellyfin = func(ctx context.Context, req admin.JellyfinRequest) (admin.JellyfinAnswer, error) {
				url, key, ans, err := takeOver(ctx, db, fixedURL, req, logger)
				if ans.Done {
					cfg.Jellyfin.URL, cfg.Secrets.JellyfinAdminKey = url, key
					ownSettings(ctx, db, url, key, logger)
				}
				return ans, forPage(err)
			}
		}
		if _, _, set, err := db.Languages(ctx); err != nil {
			return err
		} else if !set {
			setup.Language = func(ctx context.Context, tag string) error {
				_, err := db.SetLanguages(ctx, []string{tag}, tag)
				return err
			}
		}
		// TMDB's token, unless there's one: the step may be skipped, and
		// Overfin then runs on Jellyfin's data.
		if _, ok, err := db.TMDBToken(ctx); err == nil && !ok && cfg.Secrets.TMDBToken == "" {
			setup.TMDB = func(ctx context.Context, token string) (string, error) {
				token = strings.TrimSpace(token)
				if _, err := checkTMDBToken(ctx, db, token); err != nil {
					return "", forPage(err)
				}
				if err := db.SetTMDBToken(ctx, token); err != nil {
					return "", err
				}
				return "TMDB takes the token.", nil
			}
		}
		// Seerr, unless it's connected: the step may be skipped.
		if _, _, ok, err := db.Seerr(ctx); err == nil && !ok {
			setup.Seerr = func(ctx context.Context, url, key string) (string, error) {
				url = strings.TrimRight(strings.TrimSpace(url), "/")
				key = strings.TrimSpace(key)
				_, notes, err := checkSeerr(ctx, url, key)
				if err != nil {
					return "", forPage(err)
				}
				if err := db.SetSeerr(ctx, url, key); err != nil {
					return "", err
				}
				return "Seerr's API key works." + notes, nil
			}
		}
		// Intro Skipper, unless it's there.
		setup.IntroSkipper = skipper
		if haveJellyfin {
			if st, err := skipper.Look(ctx); err != nil || st.Installed {
				setup.IntroSkipper = nil
			}
		}
		if !haveAdmin {
			setup.FirstAdmin = func(ctx context.Context, email, hash string) error {
				u, err := db.CreateUser(ctx, email, true)
				if err != nil {
					return err
				}
				return db.SetPassword(ctx, u.Subject, hash)
			}
		}
		where := []any{"code", setup.Code}
		if backendMode {
			where = []any{"panel", cfg.Admin.Listen}
			logger.Warn("Overfin isn't set up yet: open the control panel's address from your LAN or VPN (port 8081 by default) and finish setting it up", where...)
		}
		if adminSrv != nil {
			panel.set(setup.Handler())
			where = append(where, "panel", cfg.Admin.Listen)
		}
		if managed && adminSrv == nil {
			// admin.private keeps the setup page to private addresses too,
			// like the panel it comes before.
			if cfg.Admin.Private {
				site.set(setup.PrivateSiteHandler())
			} else {
				site.set(setup.PublicHandler())
			}
			where = append(where, "site", cmp.Or(cfg.Site.URL, addr))
		}
		if !backendMode {
			logger.Warn("Overfin isn't set up yet: open its address (/admin/) and finish setting it up with this one-time code", where...)
		}
		select {
		case <-done:
		case err := <-errc:
			return err
		case <-ctx.Done():
			return shutdown()
		}
		site.set(settingUp)
	}

	// Its Jellyfin and plugin must be the ones this Overfin is made with
	// (a required dependency): until they are, every page says the server
	// is being updated.
	held := false
	hold := func(h http.Handler) {
		held = true
		site.set(h)
		panel.set(h)
	}
	if err := waitForCompanion(ctx, cfg.Jellyfin.URL, cfg.Secrets.JellyfinAdminKey, hold, logger); err != nil {
		return shutdown()
	}
	if held {
		site.set(settingUp)
		panel.set(http.NotFoundHandler())
	}

	sh, err := newShared(cfg, db, moduleDBs, k, logger)
	if err != nil {
		return err
	}
	sh.jellyfinSaved = jellyfinSaved
	// Email, as the panel set it, unless the configuration file does.
	if !cfg.Mail.Enabled() {
		if raw, pw, ok, err := db.Mail(ctx); err != nil {
			logger.Warn("email's settings can't be read: set email up again in the control panel", "err", err)
		} else if ok {
			var m config.Mail
			if err := json.Unmarshal(raw, &m); err != nil {
				logger.Warn("email's settings can't be read: set email up again in the control panel", "err", err)
			} else {
				sh.mail.Store(senderFor(m, pw, db))
			}
		}
	}
	// Seerr, if the panel connected it. Settings sealed with a lost secret
	// key can't be read: Overfin starts without it, and says so.
	if url, key, ok, err := db.Seerr(ctx); err != nil {
		logger.Warn("Seerr's settings can't be read: connect Seerr again in the control panel", "err", err)
	} else if ok {
		if c, err := seerr.New(url, key); err == nil {
			sh.seerr, sh.seerrURL, sh.seerrKey = c, url, key
		}
	}
	// TMDB is asked in the default language's regional tag; an install
	// from before asked in two letters, and fetches its titles again once.
	if changed, err := db.SyncTMDBLanguage(ctx); err != nil {
		logger.Warn("TMDB's language couldn't be brought in line", "err", err)
	} else if changed {
		logger.Info("TMDB is asked in the default language's region now: titles are fetched again")
	}
	// TMDB itself, with the operator's own key, in the default language.
	// Without one, Overfin runs on Jellyfin's data (and Seerr's, when it's
	// connected).
	tmdbToken := cfg.Secrets.TMDBToken
	if tmdbToken == "" {
		if k, ok, err := db.TMDBToken(ctx); err != nil {
			logger.Warn("TMDB's token can't be read: give it again in the control panel", "err", err)
		} else if ok {
			tmdbToken = k
		}
	}
	if tmdbToken != "" {
		if c, err := tmdbClient(db, tmdbToken); err == nil {
			sh.tmdb = c
		} else {
			logger.Warn("TMDB's token isn't a read access token: give it again in the control panel")
		}
	} else {
		logger.Warn("no TMDB token: Overfin runs on Jellyfin's data; give one in the control panel (Server, TMDB)")
	}
	// What TMDB says about the library's titles, kept in the background
	// (started below) and fetched on demand by the site's pages, which is
	// why it exists before them. Its source is the gateway's, once there is
	// one.
	var gw *gateway
	// Seek-bar previews, made by Overfin itself next to Jellyfin (package
	// previews): by file, not by Jellyfin's item.
	// The libraries' folders, as Jellyfin lists them: the only files the
	// backend reads (rule 78).
	libraryFolders := libraryFolders(sh.jf, func() jellyfin.Auth { return gw.adminAuth() })
	if ownPreviews() {
		st, err := previews.NewStore(filepath.Join(filepath.Dir(cfg.Storage.Path), "previews"))
		if err != nil {
			return err
		}
		sh.previews = &previews.Keeper{Store: st, DB: db, Format: previews.Default, Every: 10 * time.Minute, Logger: logger,
			JellyfinData: os.Getenv("JELLYFIN_DATA_DIR"), On: db.Previews,
			Files: func(ctx context.Context, each func(jellyfin.VideoFile) bool) error {
				return sh.jf.VideoFiles(ctx, gw.adminAuth(), each)
			},
			Libraries: libraryFolders,
			Hardware: func(ctx context.Context) []string {
				family, device, err := db.Transcoding(ctx)
				if err != nil {
					return nil
				}
				f, ok := gpu.Find(family)
				if !ok {
					return nil
				}
				return previews.HardwareFor(f.Mode, device)
			},
			Maker: previews.Maker{FFmpeg: os.Getenv("JELLYFIN_FFMPEG"), Busy: func() bool { return sh.tickets.Transcodes() > 0 }},
			// New sets reach players through the copies: their layouts put
			// on now, not at the copy index's next pass.
			Made: func() { sh.copies.Wake() }}
	}
	// The copy index: every version as an MSFP copy, its file's headers
	// read by Overfin with the image's ffprobe, in a pass of its own
	// (package copies). Nobody sees it yet: players and peers still get
	// Jellyfin's answers.
	if ownPreviews() {
		var layout func(string) (*wire.PreviewLayout, bool)
		if sh.previews != nil {
			layout = func(fp string) (*wire.PreviewLayout, bool) {
				set, ok := sh.previews.Store.Get(fp)
				if !ok {
					return nil, false
				}
				return &wire.PreviewLayout{TileWidth: set.Width, TileHeight: set.Height, Columns: set.Columns, Rows: set.Rows,
					IntervalMs: set.IntervalMs, TileCount: set.Count}, true
			}
		}
		sh.copies = &copies.Keeper{DB: db, CopyKey: sh.keys.CopyID, EtagKey: sh.keys.Etag, Every: 30 * time.Minute, Logger: logger,
			Prober: &describe.Prober{FFprobe: filepath.Join(filepath.Dir(os.Getenv("JELLYFIN_FFMPEG")), "ffprobe"), Frames: true,
				Nice: 19}, // the lowest priority
			Files: func(ctx context.Context, each func(jellyfin.VideoFile) bool) error {
				return sh.jf.VideoFiles(ctx, gw.adminAuth(), each)
			},
			Libraries: libraryFolders, Layout: layout}
		// Plays of those copies: the file from disk, or Jellyfin's
		// transcoder through the companion plugin (package delivery).
		sh.delivery = &delivery.Executor{
			Maker: &delivery.PluginMaker{Jellyfin: sh.jf, Admin: sh.service.Admin},
			Path: func(ctx context.Context, item, source string) (string, error) {
				f, err := sh.jf.VideoFileOf(ctx, gw.adminAuth(), item, source)
				return f.Path, err
			},
			Libraries: libraryFolders,
			Fingerprint: func(ctx context.Context, item, source string, f *files.File) (string, error) {
				return files.Cache{DB: db}.Fingerprint(ctx, item, source, f, time.Now())
			},
		}
	}
	sh.texts = catalog.NewTexts(func(language string) catalog.TitleSource { return gw.textSource(language) }, db, logger,
		100*time.Millisecond, 3*time.Second)
	sh.titles = catalog.NewTitles(func() catalog.TitleSource { return gw.titleSource() }, db, logger,
		10*time.Minute, 100*time.Millisecond)
	// Federation's engine runs for the whole run; its host plays with the
	// playbacks' handler in use (gw, once built) and wakes TMDB's
	// fetching when a paired server's titles were read.
	sh.fed.ctx = ctx
	sh.fed.playback = func() (api.Playback, bool) {
		if gw == nil {
			return api.Playback{}, false
		}
		b := gw.current.Load()
		if b == nil {
			return api.Playback{}, false
		}
		return b.playback, true
	}
	sh.fed.titles = func() { sh.titles.Wake(); sh.ratings.Wake() }
	gw, err = newGateway(*configPath, cfg, sh)
	if err != nil {
		return err
	}
	if sh.previews != nil {
		go sh.previews.Run(ctx) // it asks Jellyfin with the gateway's key
	}
	if sh.copies != nil {
		go sh.copies.Run(ctx) // so does it
	}
	if sh.delivery != nil {
		// Deliveries a run before left (stopped or crashed without
		// stopping them) end now (rule 50); the plugin's watchdog ends them
		// otherwise.
		go clearDeliveries(ctx, sh.delivery, logger)
	}
	skipper.Jellyfin = func() (*jellyfin.Client, jellyfin.Auth, bool) { return sh.jf, gw.adminAuth(), true }
	// The service account follows the configuration's admin key.
	sh.service.Provisioner = gw.Provisioner
	site.set(gw)

	// Playbacks outlive reloads, so their janitor runs for the whole run,
	// with the playbacks' handler in use at each pass (a bare one crashed
	// Overfin, keeping where an abandoned playback stopped).
	go api.RunJanitor(ctx, func() api.Playback { return gw.current.Load().playback })
	go purgeExpired(ctx, db, logger)
	// The catalog's index, read through Overfin's Jellyfin service account,
	// as Jellyfin's socket says the library changed (polling only while
	// it's out of reach), and whole once a day.
	arrivals := &api.WishArrivals{Store: db, Logger: logger}
	if wl := sh.moduleDBs["watchlist"]; wl != nil {
		arrivals.Watch = func(ctx context.Context, profileID, titleID string) error {
			return watchlist.Add(ctx, wl, profileID, titleID)
		}
	}
	cat := &catalog.Sync{Jellyfin: sh.jf, Store: db, Logger: logger,
		Service: sh.service, Admin: sh.service.Admin,
		// The plugin's feed, asked over loopback: cheap.
		Every: 5 * time.Second,
		// New titles matched, and their pages fetched, at once; wished
		// titles that arrived leave the wishlists for the watchlists.
		Read: func() {
			sh.titles.Wake()
			sh.copies.Wake() // a new episode's file is read at once
			go func() {
				if err := arrivals.Settle(ctx); err != nil {
					logger.Error("settling arrived wishes failed", "err", err)
				}
			}()
		}}
	go cat.Run(ctx)
	// Wishes, handed to Seerr when that's where they go.
	go (&api.WishesToSeerr{Store: db, Setup: gw.wishSetup, Logger: logger}).Run(ctx)
	// Titles' certifications, for kids profiles: looked for every 10
	// minutes, a tenth of a second apart.
	sh.ratings = ratings.NewKeeper(gw.ratingSource, db, logger, 10*time.Minute, 100*time.Millisecond)
	go sh.ratings.Run(ctx)
	// IMDb's data, while the operator has it on: downloaded once a day,
	// beside the database (rule 115).
	imdbData := imdb.NewKeeper(filepath.Join(filepath.Dir(cfg.Storage.Path), "imdb"), db.IMDbOn, logger)
	go imdbData.Run(ctx)
	// Discovery's lists, from TMDB: 10 pages (200 titles) per list, every
	// hour.
	sh.lists = catalog.NewLists(gw.tmdbSource, db, logger, time.Hour, 10)
	go sh.lists.Run(ctx)
	// With it, each episode's IMDb ID, asked of TMDB once: looked for every
	// 10 minutes, a tenth of a second between requests.
	go (&catalog.EpisodeIMDb{Store: db, Source: gw.episodeIMDbSource, On: db.IMDbOn, Logger: logger,
		Every: 10 * time.Minute, Pause: 100 * time.Millisecond}).Run(ctx)
	// The library's titles from TMDB: looked for every 10 minutes, a tenth
	// of a second between requests.
	go sh.titles.Run(ctx)
	// The picks' own lists, from TMDB: once a day.
	go catalog.NewPicks(gw.pickSource, db, logger, 24*time.Hour, 100*time.Millisecond).Run(ctx)
	go sh.texts.Run(ctx)
	// The logos kept before they were taken only in their language, checked
	// again once, at the titles' pace.
	go (&catalog.Logos{Store: db, Source: gw.logoSource, Logger: logger, Every: 10 * time.Minute, Pause: 100 * time.Millisecond}).Run(ctx)
	// The collections the library's films belong to, with their films.
	go (&catalog.Collections{Store: db, Source: gw.collectionSource, Titles: sh.titles, Logger: logger,
		Every: 15 * time.Minute, Pause: 100 * time.Millisecond}).Run(ctx)
	go unrestrictKids(ctx, db, gw.Provisioner, logger)
	// Each profile's data, brought over from Jellyfin once.
	// With Pocket ID, Overfin sets up its own client and groups there.
	if cn := cfg.Connector; cn.Kind == "pocketid" && cfg.OIDC.Enabled() {
		go setUpPocketID(ctx, cfg, logger)
	}
	// The library's artwork, kept on disk next to the database.
	go (&catalog.LibraryArtwork{Store: db, Images: sh.images, Logger: logger, Every: time.Hour}).Run(ctx)
	// Episodes TMDB has no picture for: a frame of their video, from
	// Overfin's plugin.
	go (&catalog.EpisodeFrames{Store: db, Jellyfin: sh.jf, Images: sh.images, Logger: logger, Every: time.Hour,
		Admin: sh.service.Admin,
		Service: func(ctx context.Context) (jellyfin.Auth, error) {
			a, _, err := sh.service.Auth(ctx)
			return a, err
		}}).Run(ctx)
	go (&personal.Importer{Jellyfin: sh.jf, Store: db, Logger: logger, Every: 10 * time.Minute}).Run(ctx)
	go sh.notices.Run(ctx)

	if a := cfg.Admin; a.Enabled() || managed {
		o := cfg.OIDC
		p := &admin.Panel{Version: Version, Features: gw, Modules: gw, Jellyfin: gw, Libraries: newLibraries(gw), Transcoding: gw, Seerr: gw, Catalog: cat, Group: a.Group, Logger: logger,
			TMDB: func() bool { return gw.tmdbSource() != nil }, TMDBTokens: gw, IntroSkipper: skipper,
			Engine:     sh.fed.current,
			Uncallable: gw,
			// A new language: artwork and discovery's lists, read again now.
			OnLanguage: func() { sh.titles.Wake(); sh.lists.Wake() },
			Store:      db, Provisioner: gw.Provisioner, Interests: gw.Interests, Playing: gw, Notices: sh.notices, IMDb: imdbData,
			// Pairing codes by email, whenever the SMTP server is set.
			CodeMail:   gw,
			FedChanges: &sh.fedChanges,
			Provider: &oidc.Provider{Issuer: o.Issuer, ClientID: o.ClientID, ClientSecret: cfg.Secrets.OIDCClientSecret,
				RedirectURL: a.RedirectURL, GroupsClaim: o.GroupsClaim, HTTP: oidc.NewClient(o.Dial)}}
		if sh.directory != nil {
			p.Directory = sh.directory
		}
		p.OwnPreviews = sh.previews != nil
		// Each film's and series' rank on the server, by its value, for
		// title pages and the Rank sort.
		go p.KeepRanks(ctx, 10*time.Minute)
		// Identification: TMDB searched as the library's titles are
		// described (TMDB itself); a title fixed by hand is fetched at once.
		p.SearchTMDB = func(ctx context.Context, query string) ([]tmdb.Title, error) {
			src := gw.titleSource()
			if src == nil {
				return nil, tmdb.ErrNotFound
			}
			titles, _, err := src.Search(ctx, query)
			return titles, err
		}
		p.Images = sh.images
		p.OnRetitled = sh.titles.Wake
		// What an app that imports episodes asks: from TMDB itself.
		p.EpisodeOrders = func(ctx context.Context, tmdbID int) (*tmdb.EpisodeOrders, error) {
			c, ok := gw.titleSource().(*tmdb.Client)
			if !ok || c == nil {
				return nil, admin.ErrNoTMDB
			}
			return c.EpisodeOrders(ctx, tmdbID)
		}
		p.ReadSeriesOrders = sh.titles.ReadOrders
		p.ChooseSeriesOrder = sh.titles.ChooseOrder
		// The genres left out are chosen among TMDB's own.
		p.TMDBGenres = func(ctx context.Context, kind string) ([]tmdb.Genre, error) {
			c, ok := gw.titleSource().(*tmdb.Client)
			if !ok || c == nil {
				return nil, admin.ErrNoTMDB
			}
			return c.Genres(ctx, kind)
		}
		// The metadata editor: TMDB's images of a title, from TMDB itself.
		p.TitleImages = func(ctx context.Context, kind string, tmdbID int) (map[string][]tmdb.Image, error) {
			c, ok := gw.titleSource().(*tmdb.Client)
			if !ok || c == nil {
				return nil, admin.ErrNoTMDB
			}
			return c.TitleImages(ctx, kind, tmdbID)
		}
		switch {
		case backendMode:
			// The backend: the panel's own login, on its own listener,
			// private addresses only; the site's accounts never open it.
			p.Provider, p.PanelAccount, p.Directory, p.JellyfinBeside = nil, db.PanelAccount, admin.Users{Store: db}, true
			go jellyfinWindow(ctx, gw.JellyfinAccess, cfg.Jellyfin.URL, logger)
			if managed {
				p.People, p.Mail = gw, gw
			}
			panel.set(p.Handler())
			// Made by the wizard meanwhile, perhaps: asked again.
			if _, _, havePanelLogin, err = db.PanelAccount(ctx); err != nil {
				return err
			}
			if !havePanelLogin {
				logger.Warn("the control panel has no login yet: make one with overfin-backend -set-panel-password (docker exec -it <backend> /overfin-backend -set-panel-password)")
			}
		case managed && a.Enabled():
			// Only on its own listener, private, which serves the site's
			// sign-in too: admins sign in there, on that address.
			p.Provider, p.SiteAdmin, p.SiteOwner, p.Directory, p.People, p.Mail = nil, gw.SiteAdmin, gw.SiteOwner, admin.Users{Store: db}, gw, gw
			panel.set(admin.PrivateOnly(withPanel(gw, p.SiteHandler(false))))
		case managed:
			// At /admin/ on the site, for its admins.
			p.Provider, p.SiteAdmin, p.SiteOwner, p.Directory, p.People, p.Mail = nil, gw.SiteAdmin, gw.SiteOwnerOnSite, admin.Users{Store: db}, gw, gw
			site.set(withPanel(gw, p.SiteHandler(a.Private)))
		default:
			panel.set(p.Handler())
		}
	}

	for {
		select {
		case err := <-errc:
			return err
		case <-hup:
			if err := gw.reload(os.Getenv); err != nil {
				logger.Error("configuration not reloaded; the running one stays", "err", err)
			} else {
				logger.Info("configuration reloaded")
			}
		case <-ctx.Done():
			return shutdown()
		}
	}
}

// newShared makes what lives for the whole run, once Jellyfin is known.
func newShared(cfg *config.Config, db *store.Store, moduleDBs map[string]*sql.DB, k *keys.Keys, logger *slog.Logger) (*shared, error) {
	jf, err := jellyfin.New(cfg.Jellyfin.URL, Version)
	if err != nil {
		return nil, err
	}
	art := images.New(jf, 64<<20)
	art.ArtworkDir = filepath.Join(filepath.Dir(cfg.Storage.Path), "artwork")
	sh := &shared{logger: logger, db: db, moduleDBs: moduleDBs, keys: k, jf: jf,
		images: art, tickets: playback.NewTickets(24 * time.Hour), folders: api.NewFolderCache(),
		searches: &api.AccountLimiter{PerMinute: 30},
		// TMDB asked about titles not kept yet, per account: far above
		// browsing, and a series wished whole fits.
		lookups: &api.AccountLimiter{PerMinute: 120},
		service: &provision.Service{},
		// Emails, with the operator's own templates next to the database.
		emails: &emails.Writer{Dir: filepath.Join(filepath.Dir(cfg.Storage.Path), "mail-templates"), Store: db, Logger: logger},
		proof:  &proof.Checker{Key: k.Proof}}
	// Notices: account events, by email and webhook, as the panel sets.
	sh.notices = &notices.Notifier{Store: db, Emails: sh.emails, Managed: cfg.Identity.Mode == "managed",
		Site: siteURL(cfg), Logger: logger, Mail: func() notices.Sender {
			if s := sh.mail.Load(); s != nil {
				return s
			}
			return nil
		}}
	if cn := cfg.Connector; cn.Enabled() {
		sh.directory = &connector.Cache{Directory: connector.New(cn.Kind, cn.URL, cfg.Secrets.ConnectorToken, oidc.NewClient(cn.Dial))}
	}
	return sh, nil
}

// runRecovery runs -recover-tokens. It needs Overfin's Jellyfin: Overfin's
// key for it was sealed with the lost secret key too, so the operator hands
// it over again, in the terminal (SECURITY.md rule 110).
func runRecovery(cfg *config.Config, db *store.Store, moduleDBs map[string]*sql.DB, k *keys.Keys, logger *slog.Logger) error {
	ctx := context.Background()
	url, key, saved, err := db.Jellyfin(ctx)
	switch {
	case err != nil:
		if url, key, err = handOverInTerminal(ctx, db, cfg.Jellyfin.URL, os.Stdin, os.Stderr); err != nil {
			return err
		}
	case !saved:
		return errors.New("nothing to recover: no Jellyfin set up yet")
	}
	cfg.Jellyfin.URL, cfg.Secrets.JellyfinAdminKey = url, key
	sh, err := newShared(cfg, db, moduleDBs, k, logger)
	if err != nil {
		return err
	}
	return recoverTokensWith(ctx, logger, db, newProvisioner(cfg, sh), k.Check)
}

// hsts tells browsers to come back over HTTPS only, for a year: on a site
// served over HTTPS, by Overfin itself or by a proxy in front (site.url
// says https://). Its other subdomains are the operator's business.
func hsts(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		next.ServeHTTP(w, r)
	})
}

// withPanel serves the control panel at /admin/ (and what apps read, at
// /apps/) beside the site.
func withPanel(site, panel http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/admin/", panel)
	mux.Handle("/apps/", panel)
	mux.Handle("/", site)
	return mux
}

// settingUp is what the site answers until Overfin is set up.
var settingUp = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "30")
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "This site is being set up. Come back in a moment.", http.StatusServiceUnavailable)
})

// swap serves whichever handler it holds, which can change while serving:
// a notice or the setup page first, then Overfin itself.
type swap struct{ h atomic.Pointer[http.Handler] }

func newSwap(h http.Handler) *swap {
	s := &swap{}
	s.set(h)
	return s
}

func (s *swap) set(h http.Handler) { s.h.Store(&h) }

func (s *swap) ServeHTTP(w http.ResponseWriter, r *http.Request) { (*s.h.Load()).ServeHTTP(w, r) }

// purgeExpired deletes expired sessions and logins every hour. They are
// already refused when expired; this only keeps the database small.
func purgeExpired(ctx context.Context, db *store.Store, logger *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if _, err := db.DeleteExpiredSessions(ctx); err != nil && ctx.Err() == nil {
			logger.Error("purging expired sessions failed", "err", err)
		}
		if _, err := db.DeleteExpiredLogins(ctx); err != nil && ctx.Err() == nil {
			logger.Error("purging expired logins failed", "err", err)
		}
		if err := db.TrimRevokedLogins(ctx); err != nil && ctx.Err() == nil {
			logger.Error("purging old revocations failed", "err", err)
		}
		if err := db.DeleteOldEmailLinks(ctx); err != nil && ctx.Err() == nil {
			logger.Error("purging old links failed", "err", err)
		}
		if err := db.DeleteExpiredDeviceCodes(ctx); err != nil && ctx.Err() == nil {
			logger.Error("purging expired device codes failed", "err", err)
		}
		// The catalog's change log, for paired servers' readings of what
		// changed: 30 days (MSFP catalog section 3.2); one that last read
		// before gets the whole list.
		if err := db.TrimTitleChanges(ctx, time.Now().Add(-30*24*time.Hour)); err != nil && ctx.Err() == nil {
			logger.Error("trimming the catalog's changes failed", "err", err)
		}
		// Arrived wishes past a year weigh nothing; frames of episodes gone
		// from the libraries serve nothing.
		if err := db.ForgetArrivals(ctx, time.Now().AddDate(-1, 0, 0)); err != nil && ctx.Err() == nil {
			logger.Error("purging old wish arrivals failed", "err", err)
		}
		if err := db.TrimFrames(ctx, time.Now().Add(-7*24*time.Hour)); err != nil && ctx.Err() == nil {
			logger.Error("purging frames of episodes gone failed", "err", err)
		}
		if err := db.DeleteUnusedUploads(ctx); err != nil && ctx.Err() == nil {
			logger.Error("purging unused artwork uploads failed", "err", err)
		}
		// Those who registered and never confirmed their address go.
		if n, err := db.DeleteUnconfirmed(ctx, time.Now().Add(-api.UnconfirmedFor)); err != nil && ctx.Err() == nil {
			logger.Error("removing unconfirmed registrations failed", "err", err)
		} else if n > 0 {
			logger.Info("unconfirmed registrations removed", "people", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// recoverTokensWith gives every profile whose Jellyfin token can't be read (sealed
// with a lost key) a fresh one sealed with the current key, then makes the
// current key the database's key. Profiles keep their Jellyfin users, so
// their history. PINs and logins, tied to the old key, are reset.
func recoverTokensWith(ctx context.Context, logger *slog.Logger, db *store.Store, prov *provision.Provisioner, check []byte) error {
	broken, err := db.UnreadableTokens(ctx)
	if err != nil {
		return err
	}
	logger.Info("recovery: profiles with unreadable tokens", "count", len(broken))
	failed := 0
	for _, u := range broken {
		token, err := prov.Relogin(ctx, u.JellyfinUserID, u.JellyfinUsername)
		if err == nil {
			err = db.SetJellyfinToken(ctx, u.ProfileID, token)
		}
		if err != nil {
			failed++
			logger.Error("recovery: profile not recovered", "profile", prov.LogID(u.ProfileID), "err", err)
			continue
		}
		logger.Info("recovery: profile recovered", "profile", prov.LogID(u.ProfileID))
	}
	if failed > 0 {
		return fmt.Errorf("recovery: %d profiles not recovered; the key stays unaccepted, fix and run again", failed)
	}
	pins, logins, err := db.ForgetSealed(ctx)
	if err != nil {
		return err
	}
	if err := db.SetKeyCheck(ctx, check); err != nil {
		return err
	}
	logger.Info("recovery: done; PINs cleared and everyone logs in again", "pins", pins, "logins", logins)
	return nil
}

// unrestrictKids lifts Jellyfin's rating limits from kids profiles' users,
// set by Overfin before it had its own ratings: Overfin applies those now.
// What fails is tried again ten minutes later.
func unrestrictKids(ctx context.Context, db *store.Store, prov func() *provision.Provisioner, logger *slog.Logger) {
	for {
		kids, err := db.KidsProfiles(ctx)
		failed := err != nil
		for _, p := range kids {
			if p.JellyfinUserID == "" {
				continue // none of its own: nothing to lift
			}
			err = prov().Unrestrict(ctx, p.JellyfinUserID)
			if jellyfin.IsStatus(err, http.StatusNotFound) {
				err = nil // not on this server: nothing to lift
			}
			if err != nil {
				failed = true
			}
		}
		if !failed {
			return
		}
		logger.Warn("could not lift Jellyfin's limits from kids profiles yet", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Minute):
		}
	}
}

// setUpPocketID makes sure Pocket ID has Overfin's client (its ID, secret
// and return addresses: the site's and the control panel's) and the groups
// the configuration names, trying again until Pocket ID answers.
func setUpPocketID(ctx context.Context, cfg *config.Config, logger *slog.Logger) {
	cn, o := cfg.Connector, cfg.OIDC
	p := &connector.PocketID{BaseURL: cn.URL, Key: cfg.Secrets.ConnectorToken, HTTP: oidc.NewClient(cn.Dial)}
	client := connector.Client{ID: o.ClientID, Name: "Overfin", Secret: cfg.Secrets.OIDCClientSecret, LaunchURL: siteURL(cfg)}
	for _, u := range []string{o.RedirectURL, cfg.Admin.RedirectURL} {
		if u != "" {
			client.CallbackURLs = append(client.CallbackURLs, u)
		}
	}
	var groups []string
	for _, g := range []string{cfg.Admin.Group, cfg.Identity.RequiredGroup} {
		if g != "" && !slices.Contains(groups, g) {
			groups = append(groups, g)
		}
	}
	for wait := 5 * time.Second; ; wait = min(2*wait, 5*time.Minute) {
		err := p.Setup(ctx, client, groups)
		if err == nil {
			logger.Info("Pocket ID is set up for Overfin", "client", client.ID, "groups", groups)
			if email := cn.FirstAdmin; email != "" {
				if made, err := p.FirstAdmin(ctx, email, groups); err != nil {
					logger.Error("making Pocket ID's first admin failed", "err", err)
				} else if made {
					logger.Info("Pocket ID's first admin is made (connector.first_admin); " +
						"Pocket ID's own command, pocket-id one-time-access-token followed by their email, gives them a sign-in link")
				}
			}
			if s, err := p.Signups(ctx); err == nil && s == "disabled" {
				logger.Warn("Pocket ID's sign-ups are disabled, so invitation links can't work: set ALLOW_USER_SIGNUPS=withToken there")
			}
			return
		}
		logger.Warn("setting up Pocket ID failed; trying again", "err", err, "in", wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// backendIdentity refuses, on a backend, the sign-in a proxy in front of
// Overfin vouches for (forwardauth): visitors reach the backend through
// its gateway, which passes their headers on and tells the backend their
// own address, so a proxy trusted by address would be any visitor from
// it, naming whom they like in the user header.
func backendIdentity(cfg *config.Config) error {
	if cfg.Identity.Mode == "forwardauth" {
		return errors.New(`identity.mode "forwardauth" isn't for a backend behind its gateway: visitors' headers come through the gateway; use "managed" (the default) or "oidc"`)
	}
	return nil
}

// listenSocket listens on a unix socket at path, for the gateway beside the
// backend: one left by a former run goes first. Only this user (the
// gateway's too, in the official images) may connect.
func listenSocket(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// clearDeliveries ends every delivery the plugin has, once it answers
// (Jellyfin may start after Overfin): tried for a few minutes.
func clearDeliveries(ctx context.Context, e *delivery.Executor, logger *slog.Logger) {
	for range 10 {
		err := e.Clear(ctx)
		if err == nil || jellyfin.IsStatus(err, http.StatusNotFound) {
			return // cleared, or a plugin without deliveries
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
	logger.Warn("could not clear the plugin's deliveries at start; its watchdog ends them")
}
