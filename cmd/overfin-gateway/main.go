// Command overfin-gateway is Overfin's gateway, the entrypoint of the
// overfin-gateway image: the only part the internet reaches. It passes
// visitors' requests to its backend, the routes the API specification
// lists and the site's pages only (internal/gateway). It holds no data and
// no secret of Overfin's.
//
// Two ways to run it:
//
//   - Beside its backend, on the same machine (OVERFIN_LOCAL_SOCKET): the
//     bundled gateway every backend has. It reaches the backend through a
//     socket in a volume the two share. Local (the default): plain http on
//     :8080, for private addresses only (the LAN, a VPN). Online, with
//     OVERFIN_DOMAIN or a trusted proxy in front: the site for everyone,
//     and its LAN site still, plain http on OVERFIN_LAN_LISTEN.
//   - Anywhere else (a VPS, say): a remote gateway. Its backend connects
//     out to it and they pair (internal/link): the backend has one at most.
//
// Settings, from the environment (all optional but the pairing of a remote
// gateway, once):
//
//	OVERFIN_LOCAL_SOCKET         beside the backend: the socket it serves on
//	OVERFIN_LAN_LISTEN           beside the backend, online: the LAN site's
//	                             own listener (:8090), never published to
//	                             the internet
//	OVERFIN_DOMAIN               the site's domain: HTTPS with Let's Encrypt
//	                             (on :8443, the link shares it; :8080 then
//	                             answers Let's Encrypt and sends visitors to
//	                             HTTPS). Without it, plain http on :8080.
//	OVERFIN_EMAIL                for Let's Encrypt (optional)
//	OVERFIN_LINK_LISTEN          a listener of the link's own (default :8444
//	                             without a domain; none with one)
//	OVERFIN_PAIRING              the code the backend made, to pair once
//	OVERFIN_PAIRING_FILE         or a file holding it (one machine)
//	OVERFIN_PROXY_PROTOCOL_FROM  TCP proxies in front that send the PROXY
//	                             protocol (addresses or ranges)
//	OVERFIN_TRUSTED_PROXIES      HTTP proxies in front whose X-Forwarded-For
//	                             names the visitor
//	OVERFIN_CACHE_MAX            the image cache's size at most (10GB)
//	OVERFIN_GATEWAY_DATA         its folder: /data by default
package main

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/overfin/server/internal/gateway"
	"github.com/overfin/server/internal/link"
)

// Version is the gateway's version, set at build time: the same as its
// backend's, or the link refuses it.
var Version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	dir := cmp.Or(os.Getenv("OVERFIN_GATEWAY_DATA"), "/data")
	proxyFrom, err := gateway.ParsePrefixes(os.Getenv("OVERFIN_PROXY_PROTOCOL_FROM"))
	if err != nil {
		return errors.New("OVERFIN_PROXY_PROTOCOL_FROM: " + err.Error())
	}
	if gateway.HTTPProxies, err = gateway.ParsePrefixes(os.Getenv("OVERFIN_TRUSTED_PROXIES")); err != nil {
		return errors.New("OVERFIN_TRUSTED_PROXIES: " + err.Error())
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	errc := make(chan error, 4)
	domain := strings.TrimSpace(os.Getenv("OVERFIN_DOMAIN"))
	socket := os.Getenv("OVERFIN_LOCAL_SOCKET")
	var g *link.Gateway
	var site http.Handler
	if socket != "" {
		// Beside its backend: no link, no cache, no events.
		logger.Info("Overfin's gateway starting, beside its backend", "version", Version)
		site = gateway.Handler(gateway.NewSocket(socket, Version), Version, nil, nil, logger)
	} else {
		if g, err = link.NewGateway(filepath.Join(dir, "link"), Version, pairingCode, logger); err != nil {
			return err
		}
		logger.Info("Overfin's gateway starting", "version", Version)
		revoked := &gateway.Revocations{}
		cacheMax, err := parseBytes(cmp.Or(os.Getenv("OVERFIN_CACHE_MAX"), "10GB"))
		if err != nil {
			return errors.New("OVERFIN_CACHE_MAX: " + err.Error())
		}
		cache := &gateway.Cache{Dir: filepath.Join(dir, "cache"), Limit: cacheMax, Margin: 0.10, Logger: logger}
		cache.Load()
		revoked.New = cache.Ahead
		revoked.Access = cache.ForgetAccess
		go cache.FetchAhead(ctx, g)
		go revoked.Follow(ctx, g, logger)
		site = gateway.Handler(g, Version, revoked, cache, logger)
	}
	listen := func(addr string) (net.Listener, error) {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, err
		}
		if len(proxyFrom) > 0 {
			return &gateway.ProxyListener{Listener: ln, From: proxyFrom}, nil
		}
		return ln, nil
	}

	var servers []*http.Server
	plain := &http.Server{Handler: site, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	linkAddr := os.Getenv("OVERFIN_LINK_LISTEN")
	// Beside the backend, online (a domain, or a proxy in front): the LAN
	// keeps a plain-http site of its own; local, :8080 is that site.
	online := domain != "" || len(proxyFrom) > 0 || len(gateway.HTTPProxies) > 0
	if socket != "" {
		linkAddr = ""
		if !online {
			plain.Handler = gateway.PrivateOnly(site)
		} else {
			lanAddr := cmp.Or(os.Getenv("OVERFIN_LAN_LISTEN"), ":8090")
			ln, err := net.Listen("tcp", lanAddr)
			if err != nil {
				return err
			}
			lan := &http.Server{Handler: gateway.PrivateOnly(site), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
			go func() {
				logger.Info("listening for the LAN", "addr", lanAddr)
				errc <- lan.Serve(ln)
			}()
			servers = append(servers, lan)
		}
	}
	if domain != "" {
		certs := &autocert.Manager{Prompt: autocert.AcceptTOS, HostPolicy: autocert.HostWhitelist(domain),
			Cache: autocert.DirCache(filepath.Join(dir, "certificates")), Email: os.Getenv("OVERFIN_EMAIL")}
		secure := &http.Server{Handler: hsts(site), TLSConfig: certs.TLSConfig(),
			ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
			ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelDebug)}
		secure.TLSConfig.MinVersion = tls.VersionTLS12
		if g != nil {
			g.Share(secure) // the backend's connection on the same port, by ALPN
		}
		ln, err := listen(":8443")
		if err != nil {
			return err
		}
		go func() {
			logger.Info("listening, HTTPS", "addr", ":8443", "domain", domain)
			errc <- secure.ServeTLS(ln, "", "")
		}()
		servers = append(servers, secure)
		// :8080: Let's Encrypt's checks, and visitors sent to HTTPS.
		plain.Handler = certs.HTTPHandler(nil)
	} else if g != nil {
		linkAddr = cmp.Or(linkAddr, ":8444")
	}
	ln, err := listen(":8080")
	if err != nil {
		return err
	}
	go func() {
		logger.Info("listening", "addr", ":8080", "https", domain != "", "lanOnly", socket != "" && !online)
		errc <- plain.Serve(ln)
	}()
	servers = append(servers, plain)
	if linkAddr != "" {
		go func() {
			logger.Info("listening for the backend", "addr", linkAddr)
			errc <- g.Listen(ctx, linkAddr)
		}()
	}

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if g != nil {
		g.Close()
	}
	for _, s := range servers {
		_ = s.Shutdown(shutdownCtx)
	}
	return nil
}

// parseBytes reads a size: a number of bytes, or with MB, GB or TB.
func parseBytes(s string) (int64, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	mult := int64(1)
	for suffix, m := range map[string]int64{"KB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30, "TB": 1 << 40} {
		if strings.HasSuffix(s, suffix) {
			s, mult = strings.TrimSpace(strings.TrimSuffix(s, suffix)), m
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("a size like 10GB")
	}
	return n * mult, nil
}

// hsts tells browsers to come back over HTTPS only.
func hsts(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=63072000")
		next.ServeHTTP(w, r)
	})
}

// pairingCode is the code the operator gave the gateway: OVERFIN_PAIRING,
// or the file OVERFIN_PAIRING_FILE names.
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
