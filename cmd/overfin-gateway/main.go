// Command overfin-gateway is Overfin's gateway, the entrypoint of the
// overfin-gateway image: the only part the internet reaches. Its backend
// connects out to it (internal/link); the gateway passes visitors'
// requests to it over that link, the routes the API specification lists
// and the site's pages only (internal/gateway). It holds no data and no
// secret of Overfin's.
//
// Settings, from the environment (all optional but the pairing, once):
//
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
	g, err := link.NewGateway(filepath.Join(dir, "link"), Version, pairingCode, logger)
	if err != nil {
		return err
	}
	logger.Info("Overfin's gateway starting", "version", Version)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	errc := make(chan error, 4)
	site := gateway.Handler(g, Version, logger)
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

	domain := strings.TrimSpace(os.Getenv("OVERFIN_DOMAIN"))
	var servers []*http.Server
	plain := &http.Server{Handler: site, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	linkAddr := os.Getenv("OVERFIN_LINK_LISTEN")
	if domain != "" {
		certs := &autocert.Manager{Prompt: autocert.AcceptTOS, HostPolicy: autocert.HostWhitelist(domain),
			Cache: autocert.DirCache(filepath.Join(dir, "certificates")), Email: os.Getenv("OVERFIN_EMAIL")}
		secure := &http.Server{Handler: hsts(site), TLSConfig: certs.TLSConfig(),
			ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
			ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelDebug)}
		secure.TLSConfig.MinVersion = tls.VersionTLS12
		g.Share(secure) // the backend's connection on the same port, by ALPN
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
	} else {
		linkAddr = cmp.Or(linkAddr, ":8444")
	}
	ln, err := listen(":8080")
	if err != nil {
		return err
	}
	go func() {
		logger.Info("listening", "addr", ":8080", "https", domain != "")
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
	g.Close()
	for _, s := range servers {
		_ = s.Shutdown(shutdownCtx)
	}
	return nil
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
