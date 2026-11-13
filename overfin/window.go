package overfin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/overfin/server/internal/admin"
)

// jellyfinWindowAddr is where the backend serves its Jellyfin's own pages
// while access to it is open (control panel, Server, Jellyfin): Jellyfin
// itself listens inside the container only (127.0.0.1).
var jellyfinWindowAddr = ":8082"

// jellyfinWindow watches whether access to Jellyfin is open and, while it
// is, serves Jellyfin's pages on jellyfinWindowAddr, for private addresses
// only (the LAN, a VPN), like the control panel; closed, the listener goes.
func jellyfinWindow(ctx context.Context, access func(context.Context) (bool, time.Time, error), jellyfinURL string, logger *slog.Logger) {
	target, err := url.Parse(jellyfinURL)
	if err != nil {
		logger.Error("Jellyfin's pages: its address", "err", err)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	var srv *http.Server
	stop := func() {
		if srv != nil {
			shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = srv.Shutdown(shut)
			cancel()
			srv = nil
			logger.Info("Jellyfin's pages are no longer served (access closed)")
		}
	}
	defer stop()
	for {
		open, _, err := access(ctx)
		switch {
		case err != nil:
		case open && srv == nil:
			srv = &http.Server{Addr: jellyfinWindowAddr, Handler: admin.PrivateOnly(proxy), ReadHeaderTimeout: 10 * time.Second}
			s := srv
			go func() {
				if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Error("Jellyfin's pages couldn't be served", "addr", jellyfinWindowAddr, "err", err)
				}
			}()
			logger.Warn("access to Jellyfin is open: its pages are served on the LAN, port 8082, until it's closed")
		case !open:
			stop()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}
