package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/overfin/server/internal/feature"
	"github.com/overfin/server/internal/invites"
)

// The panel's side of invitations: invite templates made, listed,
// changed and deleted, each bound checked; an invitation's "for whom" kept
// plain; a deleted invitation's link no longer opens.
func TestInvitesAdmin(t *testing.T) {
	var tmpl string
	panel := http.NewServeMux()
	srv, _ := newServerWith(t, options{modules: []feature.Feature{invites.Feature{}}, template: &tmpl, admin: panel})
	admin := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		var b []byte
		if s, ok := body.(string); ok {
			b = []byte(s)
		} else if body != nil {
			b, _ = json.Marshal(body)
		}
		rec := httptest.NewRecorder()
		panel.ServeHTTP(rec, httptest.NewRequest(method, "/admin/api/modules/invites"+path, bytes.NewReader(b)))
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	ok := map[string]any{"name": "Friends", "accountTemplate": tmpl, "accessDays": 30, "validDays": 7, "maxUses": 1}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for a, b := range ok {
			m[a] = b
		}
		m[k] = v
		return m
	}

	// The account templates offered.
	if code, out := admin("GET", "/account-templates", nil); code != 200 || !strings.Contains(mustJSON(out), tmpl) {
		t.Errorf("account templates: %d %v", code, out)
	}
	// Each bound.
	for name, body := range map[string]any{
		"empty name":        with("name", ""),
		"name too long":     with("name", strings.Repeat("x", 65)),
		"name spaced":       with("name", " Friends"),
		"name with control": with("name", "Fri\x07ends"),
		"name with bidi":    with("name", "Fri\u202eends"),
		"access -1":         with("accessDays", -1),
		"access 3651":       with("accessDays", 3651),
		"valid -1":          with("validDays", -1),
		"valid 3651":        with("validDays", 3651),
		"uses -1":           with("maxUses", -1),
		"uses 100001":       with("maxUses", 100001),
		"unknown member":    with("extra", true),
		"not json":          "{",
	} {
		if code, out := admin("POST", "/templates", body); code != 400 {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	code, it := admin("POST", "/templates", ok)
	if code != 200 || it["id"] == nil {
		t.Fatalf("a template: %d %v", code, it)
	}
	id := it["id"].(string)
	if code, _ := admin("POST", "/templates", ok); code != 400 {
		t.Errorf("the same name twice: %d", code)
	}
	// Bounds at their edges are fine.
	edges := with("name", strings.Repeat("é", 64))
	edges["accessDays"], edges["validDays"], edges["maxUses"] = 3650, 0, 0
	if code, out := admin("POST", "/templates", edges); code != 200 {
		t.Errorf("edges: %d %v", code, out)
	}
	// Changed, then listed.
	if code, out := admin("PUT", "/templates/"+id, with("name", "Family")); code != 200 || out["name"] != "Family" {
		t.Errorf("change: %d %v", code, out)
	}
	if code, _ := admin("PUT", "/templates/itmpl_nothing", ok); code != 404 {
		t.Errorf("change of none: %d", code)
	}
	_, list := admin("GET", "/templates", nil)
	if s := mustJSON(list); !strings.Contains(s, `"Family"`) || strings.Contains(s, `"Friends"`) {
		t.Errorf("templates: %s", s)
	}

	// An invitation: its label plain text only.
	if code, _ := admin("POST", "/invites", map[string]any{"template": id, "label": "Bob\u202e"}); code != 400 {
		t.Errorf("a label with bidi: %d", code)
	}
	if code, _ := admin("POST", "/invites", map[string]any{"template": "itmpl_none", "label": "Bob"}); code != 400 {
		t.Errorf("an unknown template: %d", code)
	}
	code, made := admin("POST", "/invites", map[string]any{"template": id, "label": "Bob"})
	if code != 200 {
		t.Fatalf("invitation: %d %v", code, made)
	}
	link := strings.TrimPrefix(made["link"].(string), "https://media.example.org")
	inv := made["invite"].(map[string]any)
	if inv["maxUses"] != 1.0 || inv["accessDays"] != 30.0 || inv["expiresAt"] == "" || inv["label"] != "Bob" {
		t.Errorf("the invitation: %v", inv)
	}
	// Its code is never listed again.
	_, invs := admin("GET", "/invites", nil)
	if code := strings.TrimPrefix(link, "/invite/"); strings.Contains(mustJSON(invs), code) {
		t.Error("the list shows an invitation's code")
	}
	// Deleting the template leaves the invitation; deleting the invitation
	// ends its link.
	if code, _ := admin("DELETE", "/templates/"+id, nil); code != 204 {
		t.Errorf("delete template: %d", code)
	}
	if res, page := openInvitation(t, srv, link); res.StatusCode != 200 || !strings.Contains(page, "invited to join") {
		t.Errorf("the link after its template went: %d", res.StatusCode)
	}
	if code, _ := admin("DELETE", "/invites/"+inv["id"].(string), nil); code != 204 {
		t.Errorf("delete invitation: %d", code)
	}
	if _, page := openInvitation(t, srv, link); !strings.Contains(page, "isn&#39;t valid") {
		t.Error("a deleted invitation's link still opens")
	}
	// Uses since: a time is required.
	for _, q := range []string{"", "?since=", "?since=x", "?since=0", "?since=-5"} {
		if code, _ := admin("GET", "/uses"+q, nil); code != 400 {
			t.Errorf("uses%s: %d", q, code)
		}
	}
	if code, out := admin("GET", "/uses?since=1", nil); code != 200 || out["items"] == nil {
		t.Errorf("uses: %d %v", code, out)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
