package overfin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Overfin's Jellyfin must be the version it's made with, with its plugin
// at the version it's made with; the plugin is given Overfin's key first.
func TestCheckCompanion(t *testing.T) {
	const key = "overfins-own-key"
	sum := sha256.Sum256([]byte(key))
	want := hex.EncodeToString(sum[:])
	for _, tc := range []struct {
		name            string
		version, plugin string // "" plugin: not installed
		problem         string
	}{
		{"right", JellyfinVersion, PluginVersion, ""},
		{"another Jellyfin", "12.1.0", PluginVersion, "version 12.1.0"},
		{"no plugin", JellyfinVersion, "", "plugin isn't on"},
		{"another plugin", JellyfinVersion, "0.0.9", "plugin on Overfin's Jellyfin is version 0.0.9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotHash string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/System/Info/Public":
					json.NewEncoder(w).Encode(map[string]any{"Version": tc.version, "Id": "x", "StartupWizardCompleted": true})
				case r.URL.Path == "/Plugins/"+pluginID+"/Configuration" && r.Method == http.MethodPost && tc.plugin != "":
					var body map[string]string
					json.NewDecoder(r.Body).Decode(&body)
					gotHash = body["KeyHash"]
					w.WriteHeader(http.StatusNoContent)
				case r.URL.Path == "/Overfin/Info" && tc.plugin != "":
					json.NewEncoder(w).Encode(map[string]string{"Plugin": tc.plugin, "Jellyfin": tc.version})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			err := checkCompanion(context.Background(), srv.URL, key)
			if tc.problem == "" {
				if err != nil || gotHash != want {
					t.Fatalf("%v, hash %q", err, gotHash)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.problem) {
				t.Fatalf("%v", err)
			}
		})
	}
}

// The page shown meanwhile names nothing behind the site.
func TestUpdatingPage(t *testing.T) {
	rec := httptest.NewRecorder()
	updating.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 503 || strings.Contains(strings.ToLower(rec.Body.String()), "jellyfin") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// Against a real Jellyfin from the overfin-jellyfin image, when one is given
// (OVERFIN_LIVE_JELLYFIN_URL, and the key's file in
// OVERFIN_LIVE_JELLYFIN_KEY_FILE): skipped otherwise.
func TestCheckCompanionLive(t *testing.T) {
	url, file := os.Getenv("OVERFIN_LIVE_JELLYFIN_URL"), os.Getenv("OVERFIN_LIVE_JELLYFIN_KEY_FILE")
	if url == "" || file == "" {
		t.Skip("no live Jellyfin given")
	}
	key, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkCompanion(context.Background(), url, strings.TrimSpace(string(key))); err != nil {
		t.Fatal(err)
	}
}
