package overfin

import (
	"testing"

	"github.com/overfin/server/internal/config"
)

// A backend never takes a proxy's word for who signs in: its visitors come
// through its gateway, headers and all.
func TestBackendIdentity(t *testing.T) {
	for mode, ok := range map[string]bool{"forwardauth": false, "managed": true, "oidc": true, "anonymous": true} {
		cfg := &config.Config{Identity: config.Identity{Mode: mode}}
		if err := backendIdentity(cfg); (err == nil) != ok {
			t.Errorf("%s: %v", mode, err)
		}
	}
}
