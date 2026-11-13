// Command overfin-backend is Overfin's backend, the entrypoint of the
// overfin-backend image: it runs next to the media, with Jellyfin and
// Overfin's plugin for it. For now it puts the plugin in place, keeps
// Jellyfin to the container, and runs it (see internal/backend).
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/overfin/server/internal/backend"
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
	jf.Supervise(ctx, logger)
	logger.Info("Overfin's backend stopped")
	return nil
}
