// Command overfin-gateway is Overfin's gateway, the entrypoint of the
// overfin-gateway image: the only part the internet reaches. Its backend
// connects out to it (internal/link); the gateway passes visitors'
// requests to it over that link.
//
// For now (step 3 of the gateway/backend split) it passes every request
// on, with the visitor's address; the route allowlist, HTTPS, limits, the
// cache and its pages come in step 4.
//
// Settings, from the environment:
//
//	OVERFIN_GATEWAY_DATA   its folder (key, pairing): /data by default
//	OVERFIN_GATEWAY_LISTEN the site's address: :8080 by default
//	OVERFIN_LINK_LISTEN    where the backend connects: :8443 by default
//	OVERFIN_PAIRING        the code the backend made, to pair once
//	OVERFIN_PAIRING_FILE   or a file holding it (one machine, shared volume)
package main

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/overfin/server/internal/link"
	overfin "github.com/overfin/server/overfin"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	dir := cmp.Or(os.Getenv("OVERFIN_GATEWAY_DATA"), "/data")
	g, err := link.NewGateway(filepath.Join(dir, "link"), overfin.Version, pairingCode, logger)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	errc := make(chan error, 2)
	linkAddr := cmp.Or(os.Getenv("OVERFIN_LINK_LISTEN"), ":8443")
	go func() {
		logger.Info("listening for the backend", "addr", linkAddr)
		errc <- g.Listen(ctx, linkAddr)
	}()

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = "http", "backend"
			// Only the gateway says who the visitor is.
			pr.Out.Header.Del("X-Forwarded-For")
			pr.Out.Header.Del("Forwarded")
			pr.Out.Header.Set(link.ClientAddr, pr.In.RemoteAddr)
		},
		Transport:     g,
		FlushInterval: -1, // streams pass as they come
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, link.ErrNoBackend) {
				http.Error(w, "The server can't be reached right now. Try again in a moment.", http.StatusServiceUnavailable)
				return
			}
			http.Error(w, "The server didn't answer.", http.StatusBadGateway)
		},
	}
	addr := cmp.Or(os.Getenv("OVERFIN_GATEWAY_LISTEN"), ":8080")
	srv := &http.Server{Addr: addr, Handler: proxy, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	go func() {
		logger.Info("listening", "addr", addr)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	g.Close()
	_ = srv.Shutdown(shutdownCtx)
	return nil
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
