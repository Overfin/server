package api

import (
	"os"
	"testing"

	"github.com/overfin/server/internal/outbound"
	"github.com/overfin/server/internal/password"
	"github.com/overfin/server/internal/proof"
)

// TestMain makes password hashes and the pages' checks cheap: at their
// real cost, the sign-in tests took most of the package's time. Paired
// servers in the tests are on loopback, which Overfin otherwise refuses
// to dial for a peer (internal/outbound).
func TestMain(m *testing.M) {
	outbound.AllowLoopback(true)
	password.CheapForTests()
	proof.CheapForTests()
	os.Exit(m.Run())
}
