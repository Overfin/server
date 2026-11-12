package overfin

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/keys"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

// publicRoutes are the routes SECURITY.md rule 37 names, in its words.
var publicRoutes = map[string]string{
	"GET /api/v1/health": "health",
	// The stream's media checks its ticket itself (rule 30), and
	// federation's calls answer paired servers' signed requests only (rule
	// 65).
	"GET /api/v1/stream/{ticket}/{name}":            "the stream's media",
	"GET /api/v1/stream/{ticket}/subtitles/{track}": "the stream's subtitles",
	"GET /api/v1/stream/{ticket}/pictures/{track}":  "the stream's picture subtitles",
	"POST /api/v1/federation/pair":                  "federation: using a pairing code",
	"POST /api/v1/federation/hello":                 "federation: where a pairing stands",
	"POST /api/v1/federation/libraries":             "federation: the libraries offered",
	"POST /api/v1/federation/titles":                "federation: the titles offered",
	"POST /api/v1/federation/play":                  "federation: a paired server's viewer plays",
	"POST /api/v1/federation/progress":              "federation: where a viewer is",
	"POST /api/v1/federation/stop":                  "federation: and stops",
	"GET /signin":                                   "the sign-in page",
	"GET /invite/{code}":                            "the invitation page",
	"GET /reset":                                    "the forgotten-password page",
	"GET /api/v1/pages/page.js":                     "their script",
	"GET /api/v1/pages/page.css":                    "their style",
	"GET /api/v1/pages/logo.svg":                    "their logo",
	"GET /api/v1/pages/challenge":                   "their check against scripts",
	"POST /api/v1/signin":                           "the sign-in calls",
	"POST /api/v1/signin/verify":                    "the sign-in calls",
	"POST /api/v1/signin/look":                      "the sign-in calls",
	"POST /api/v1/signin/resend":                    "the sign-in calls",
	"POST /api/v1/signin/password":                  "the sign-in calls",
	"POST /api/v1/register":                         "the registration call",
	"POST /api/v1/reset":                            "the forgotten-password calls",
	"POST /api/v1/reset/password":                   "the forgotten-password calls",
	"GET /setup":                                    "the setup page",
	"POST /api/v1/setup/look":                       "the setup calls",
	"POST /api/v1/setup/password":                   "the setup calls",
	"GET /api/v1/login":                             "the login's start",
	"GET /api/v1/auth/callback":                     "the identity provider's return",
	"GET /api/v1/auth/finish":                       "the login's finish",
	"GET /api/v1/gate":                              "the gate",
	"POST /api/v1/logout":                           "logout",
	"GET /api/v1/theme.css":                         "theme.css",
	"GET /api/v1/branding":                          "the operator's branding",
	"GET /api/v1/branding/{kind}":                   "the operator's branding",
	"GET /api/v1/info":                              "what this server is",
	"GET /link":                                     "the page to confirm a screen's code",
	"POST /api/v1/device/start":                     "the code sign-in's start and check",
	"POST /api/v1/device/check":                     "the code sign-in's start and check",
}

// publicLog keeps the routes the server logs as public.
type publicLog map[string]bool

func (publicLog) Enabled(context.Context, slog.Level) bool { return true }
func (l publicLog) Handle(_ context.Context, r slog.Record) error {
	var pattern string
	var public bool
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "pattern":
			pattern = a.Value.String()
		case "public":
			public = a.Value.Bool()
		}
		return true
	})
	if r.Message == "route" && public {
		l[pattern] = true
	}
	return nil
}
func (l publicLog) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l publicLog) WithGroup(string) slog.Handler      { return l }

// Every route that needs no session, in every sign-in mode, is one rule
// 37 names: a new one is checked against the rule and added to both.
func TestPublicRoutesAreRule37s(t *testing.T) {
	modes := map[string]string{
		"managed":     `mode = "managed"`,
		"forwardauth": "mode = \"forwardauth\"\ntrusted_proxies = [\"127.0.0.1\"]\nuser_header = \"Remote-User\"",
		"oidc": "mode = \"oidc\"\n[oidc]\nissuer = \"https://id.example\"\nclient_id = \"overfin\"\n" +
			`redirect_url = "https://site.example/api/v1/auth/callback"`,
	}
	for name, identity := range modes {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "overfin.db")
		cfgPath := filepath.Join(dir, "overfin.toml")
		file := "listen = \":8080\"\n[jellyfin]\nurl = \"http://192.0.2.10:8096\"\n[storage]\npath = \"" + dbPath + "\"\n[identity]\n" + identity + "\n"
		if err := os.WriteFile(cfgPath, []byte(file), 0o600); err != nil {
			t.Fatal(err)
		}
		getenv := env("a")
		cfg, err := config.Load(cfgPath, func(key string) string {
			if key == "OVERFIN_OIDC_CLIENT_SECRET" {
				return "secret"
			}
			return getenv(key)
		})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		k, _ := keys.Derive(cfg.Secrets.Key)
		box, _ := seal.New(k.TokenEncryption)
		db, err := store.Open(context.Background(), dbPath, box)
		if err != nil {
			t.Fatal(err)
		}
		jf, _ := jellyfin.New(cfg.Jellyfin.URL, "test")
		seen := publicLog{}
		sh := &shared{logger: slog.New(seen), db: db, keys: k, jf: jf,
			images: images.New(jf, 1<<20), tickets: playback.NewTickets(time.Hour)}
		if _, err := newGateway(cfgPath, cfg, sh); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		db.Close()
		if len(seen) == 0 {
			t.Fatalf("%s: no public route logged", name)
		}
		for pattern := range seen {
			if _, ok := publicRoutes[pattern]; !ok {
				t.Errorf("%s: %s needs no session but isn't in SECURITY.md rule 37 (and publicRoutes)", name, pattern)
			}
		}
	}
}
