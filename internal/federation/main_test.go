package federation

import (
	"os"
	"testing"
	"time"

	"github.com/overfin/server/internal/outbound"
)

// TestMain lets the tests' paired servers be on loopback, which Overfin
// otherwise refuses to dial for a peer (internal/outbound); the tests that
// check that refusal switch it back off.
//
// A new address that doesn't answer yet is tried again within moments.
func TestMain(m *testing.M) {
	outbound.AllowLoopback(true)
	answerRetryFirst, answerRetryMost = 10*time.Millisecond, 50*time.Millisecond
	os.Exit(m.Run())
}
