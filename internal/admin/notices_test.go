package admin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/overfin/server/internal/notices"
)

// The notices' settings: off at first, checked, saved; the webhook's key
// shown once.
func TestNoticeSettings(t *testing.T) {
	p, b, db, _ := signedInPanel(t)
	p.Notices = &notices.Notifier{Store: db, Managed: true}
	b.h = p.Handler()

	if body := b.do("GET", "/admin/api/notices", "", nil).Body.String(); !strings.Contains(body, `"days":7`) ||
		!strings.Contains(body, `"joined":false`) || !strings.Contains(body, `"signed":false`) || !strings.Contains(body, `"mail":false`) {
		t.Errorf("at first: %s", body)
	}
	for _, bad := range []string{`{"days":0}`, `{"days":61}`, `{"days":7,"webhook":"file:///etc/passwd"}`, `{"days":7,"x":1}`} {
		if rec := b.do("PUT", "/admin/api/notices", bad, nil); rec.Code != 400 {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	if rec := b.do("PUT", "/admin/api/notices", `{"joined":true,"days":3,"webhook":"https://example.org/hook"}`, nil); rec.Code != 204 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	if body := b.do("GET", "/admin/api/notices", "", nil).Body.String(); !strings.Contains(body, `"joined":true`) ||
		!strings.Contains(body, `"days":3`) || !strings.Contains(body, `"webhook":"https://example.org/hook"`) {
		t.Errorf("saved: %s", body)
	}
	rec := b.do("POST", "/admin/api/notices/secret", `{}`, nil)
	var out struct{ Secret string }
	json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || len(out.Secret) != 64 {
		t.Errorf("key: %d %s", rec.Code, rec.Body)
	}
	if body := b.do("GET", "/admin/api/notices", "", nil).Body.String(); !strings.Contains(body, `"signed":true`) || strings.Contains(body, out.Secret) {
		t.Errorf("after the key: %s", body)
	}
	// Testing with nowhere to send says so.
	if rec := b.do("POST", "/admin/api/notices/test", `{"days":7}`, nil); rec.Code != 400 || !strings.Contains(rec.Body.String(), "choose where") {
		t.Errorf("test: %d %s", rec.Code, rec.Body)
	}
}
