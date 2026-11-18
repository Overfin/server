package federation

import (
	"os"
	"testing"

	"github.com/overfin/server/internal/outbound"
)

// TestMain lets the tests' paired servers be on loopback, which Overfin
// otherwise refuses to dial for a peer (internal/outbound); the tests that
// check that refusal switch it back off.
//
// A move is announced at once: the test servers answer before it's noted.
func TestMain(m *testing.M) {
	outbound.AllowLoopback(true)
	announceAfter = 0
	os.Exit(m.Run())
}
