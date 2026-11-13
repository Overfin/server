package overfin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/overfin/server/internal/jellyfin"
)

// The Jellyfin and the plugin this Overfin is made with (the image's).
// Overfin serves nothing else until its Jellyfin is that one, with that
// plugin: a required dependency.
const (
	JellyfinVersion = "12.2.0"
	PluginVersion   = "0.2.1"
	pluginID        = "33809b90-8925-4df0-93fd-d9b06f14ca0a"
)

// checkCompanion says what's wrong with Overfin's Jellyfin and its plugin,
// nil when they're the ones this Overfin is made with. It gives the plugin
// Overfin's key first, so the plugin answers Overfin, and no one else.
func checkCompanion(ctx context.Context, url, key string) error {
	jf, err := jellyfin.New(url, Version)
	if err != nil {
		return err
	}
	pub, err := jf.Public(ctx)
	if err != nil {
		return fmt.Errorf("the Jellyfin doesn't answer: %w", err)
	}
	if pub.Version != JellyfinVersion {
		return fmt.Errorf("the Jellyfin is version %s, and this Overfin is made for %s: run its Jellyfin from the image "+
			"%s", pub.Version, JellyfinVersion, jellyfinImage())
	}
	admin := jellyfin.Auth{Token: key, DeviceID: "overfin-admin"}
	sum := sha256.Sum256([]byte(key))
	if err := jf.SetPluginKey(ctx, admin, pluginID, hex.EncodeToString(sum[:])); err != nil {
		return fmt.Errorf("the plugin isn't on Overfin's Jellyfin (run its Jellyfin from the image %s): %w", jellyfinImage(), err)
	}
	plugin, _, err := jf.PluginInfo(ctx, admin)
	if err != nil {
		return fmt.Errorf("the plugin on Overfin's Jellyfin doesn't answer: %w", err)
	}
	if plugin != PluginVersion {
		return fmt.Errorf("the plugin on Overfin's Jellyfin is version %s, and this Overfin is made for %s: run its Jellyfin from the image "+
			"%s", plugin, PluginVersion, jellyfinImage())
	}
	return nil
}

// jellyfinImage is the image of the backend (with its Jellyfin) made with this Overfin:
// the same version, tagged without its "v".
func jellyfinImage() string {
	return "ghcr.io/overfin/overfin-backend:" + strings.TrimPrefix(Version, "v")
}

// companionEvery is how often a Jellyfin or plugin that isn't right is
// checked again.
const companionEvery = 30 * time.Second

// waitForCompanion holds Overfin until its Jellyfin and plugin are right
// (checkCompanion): meanwhile, every page says the server is being
// updated, and the log says what's wrong, each time it changes. It returns
// once they're right, or with ctx.Err() when Overfin stops.
func waitForCompanion(ctx context.Context, url, key string, hold func(http.Handler), logger *slog.Logger) error {
	last := ""
	for {
		err := checkCompanion(ctx, url, key)
		if err == nil {
			if last != "" {
				logger.Info("Overfin's Jellyfin and its plugin are the right ones now")
			}
			return nil
		}
		if err.Error() != last {
			if last == "" {
				hold(updating)
			}
			last = err.Error()
			logger.Error("Overfin serves nothing until its Jellyfin and plugin are the ones it's made with: "+last,
				"jellyfin", JellyfinVersion, "plugin", PluginVersion)
		}
		select {
		case <-time.After(companionEvery):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// updating is every page while Overfin waits for its Jellyfin: words for
// anyone, naming nothing behind the site.
var updating = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Retry-After", "60")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	fmt.Fprint(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Updating</title></head>`+
		`<body style="font-family:sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem"><p>This server is being updated. Please come back in a few minutes.</p>`+
		`<p lang="fr">Ce serveur est en cours de mise à jour. Revenez dans quelques minutes.</p></body></html>`)
})
