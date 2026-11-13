// Command overfin-backend is Overfin's backend, the entrypoint of the
// overfin-backend image: it runs next to the media, with Jellyfin and
// Overfin's plugin for it, and connects out to its gateway (see
// internal/backend and internal/link).
//
// Pairing with a gateway, from the backend's machine:
//
//	docker exec <backend> /overfin-backend -pair <gateway's host:port> [-server-name <name>]
//
// prints a one-time code to set on the gateway (OVERFIN_PAIRING).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/overfin/server/internal/backend"
	"github.com/overfin/server/internal/link"
)

func main() {
	if code, ran := backend.Init(); ran {
		os.Exit(code)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	pair := flag.String("pair", "", "pair with the gateway at this host:port: prints a one-time code to set on the gateway (OVERFIN_PAIRING), then exits")
	serverName := flag.String("server-name", "", "with -pair: the name sent to the gateway in TLS, when a proxy in front of it routes on names (default: the address's host)")
	flag.Parse()

	dataDir := os.Getenv("OVERFIN_BACKEND_DATA")
	if dataDir == "" {
		dataDir = "/data"
	}
	linkDir := filepath.Join(dataDir, "link")
	if err := os.MkdirAll(linkDir, 0o700); err != nil {
		return err
	}
	lb, err := link.NewBackend(linkDir, backend.Version, backend.Routes(), logger)
	if err != nil {
		return err
	}
	if *pair != "" {
		code, err := lb.Pair(*pair, *serverName)
		if err != nil {
			return err
		}
		fmt.Printf("Pairing code for the gateway at %s:\n\n  %s\n\nSet it on the gateway as OVERFIN_PAIRING and start it again; this backend pairs at its next attempt.\n", *pair, code)
		return nil
	}

	logger.Info("Overfin's backend starting", "version", backend.Version)
	jf := backend.FromEnv(os.Getenv)
	version, err := jf.InstallPlugin()
	if err != nil {
		return err
	}
	logger.Info("Overfin's plugin is in place for Jellyfin", "version", version)
	if err := jf.LoopbackOnly(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); lb.Run(ctx) }()
	go func() { defer wg.Done(); jf.Supervise(ctx, logger) }()
	wg.Wait()
	logger.Info("Overfin's backend stopped")
	return nil
}
