package admin

import (
	"os"
	"testing"

	"github.com/overfin/server/internal/password"
	"github.com/overfin/server/internal/proof"
)

// TestMain makes password hashes and the pages' checks cheap: at their
// real cost, the sign-in tests took most of the package's time.
func TestMain(m *testing.M) {
	password.CheapForTests()
	proof.CheapForTests()
	os.Exit(m.Run())
}
