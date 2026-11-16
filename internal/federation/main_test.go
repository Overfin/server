package federation

import (
	"os"
	"testing"

	"github.com/overfin/server/internal/outbound"
)

// TestMain lets the tests' paired servers be on loopback, which Overfin
// otherwise refuses to dial for a peer (internal/outbound); the tests that
// check that refusal switch it back off.
func TestMain(m *testing.M) {
	outbound.AllowLoopback(true)
	os.Exit(m.Run())
}
