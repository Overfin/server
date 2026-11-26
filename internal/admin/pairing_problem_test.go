package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/msfp-io/msfp-go"
)

// Using a pairing code: each of the engine's refusals said plainly (400,
// the operator's next step); anything else a failure that shows nothing
// of the error.
func TestPairingProblems(t *testing.T) {
	p := &Panel{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for err, says := range map[error]string{
		msfp.ErrCodeInvalid:                            "isn't a pairing code",
		msfp.ErrPlainHTTP:                              "plain http://",
		msfp.ErrOwnCode:                                "this server's own code",
		msfp.ErrAlreadyPeer:                            "already in the list",
		msfp.ErrAlreadyMember:                          "already in that group",
		msfp.ErrNotSupported:                           "doesn't speak groups",
		msfp.ErrCodeExpired:                            "time is up",
		msfp.ErrCodeRefused:                            "refused the code",
		msfp.ErrRefusedDestination:                     "never connects to",
		msfp.ErrCertificateRefused:                     "certificate",
		fmt.Errorf("pairing: %w", msfp.ErrCodeExpired): "time is up",
	} {
		w := httptest.NewRecorder()
		p.pairingProblem(w, err)
		var out struct{ Type, Detail string }
		json.Unmarshal(w.Body.Bytes(), &out)
		if w.Code != 400 || out.Type != "invalid-request" || !strings.Contains(out.Detail, says) {
			t.Errorf("%v: %d %+v", err, w.Code, out)
		}
	}
	w := httptest.NewRecorder()
	p.pairingProblem(w, errors.New("disk full at /var/lib/overfin/secret.db"))
	if w.Code != 500 || strings.Contains(w.Body.String(), "/var/lib") || strings.Contains(w.Body.String(), "disk full") {
		t.Errorf("another error: %d %s", w.Code, w.Body)
	}
}
