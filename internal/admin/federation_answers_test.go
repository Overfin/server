package admin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/msfp-io/msfp-go"
)

// A pairing code withdrawn: listed no more; again, or one that isn't, not
// found. A peer's new key confirmed when none is waiting: said so; a
// server that isn't one, not found, for its refused key too. A group's
// request or join answered in a group that isn't: not found.
func TestFederationAnswersRefused(t *testing.T) {
	b, _, db := fedPanel(t, "A")
	rec := b.do("POST", "/admin/api/federation/codes", `{"note":"x"}`, nil)
	var made struct{ ID string }
	json.Unmarshal(rec.Body.Bytes(), &made)
	if rec.Code != 201 {
		t.Fatalf("code: %d", rec.Code)
	}
	if rec := b.do("DELETE", "/admin/api/federation/codes/"+made.ID, "", nil); rec.Code != 204 {
		t.Fatalf("withdraw: %d %s", rec.Code, rec.Body)
	}
	if s := readState(t, b); len(s.Codes) != 0 {
		t.Errorf("still listed: %+v", s.Codes)
	}
	for _, id := range []string{made.ID, "nope"} {
		if rec := b.do("DELETE", "/admin/api/federation/codes/"+id, "", nil); rec.Code != 404 {
			t.Errorf("withdraw %s: %d", id, rec.Code)
		}
	}

	putPeer(t, db, msfp.PeerRecord{ID: "peer-1", Name: "B", State: msfp.Paired})
	if rec := b.do("POST", "/admin/api/federation/peers/peer-1/confirm-key", `{}`, nil); rec.Code != 409 || !strings.Contains(rec.Body.String(), "no-pending-key") {
		t.Errorf("no key waiting: %d %s", rec.Code, rec.Body)
	}
	for _, path := range []string{"/admin/api/federation/peers/nobody/confirm-key"} {
		if rec := b.do("POST", path, `{}`, nil); rec.Code != 404 {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
	if rec := b.do("DELETE", "/admin/api/federation/peers/nobody/refused", "", nil); rec.Code != 404 {
		t.Errorf("a refused key of nobody: %d", rec.Code)
	}
	for _, path := range []string{
		"/admin/api/federation/groups/nogroup/requests/a@b.c/accept",
		"/admin/api/federation/groups/nogroup/requests/a@b.c/refuse",
		"/admin/api/federation/groups/nogroup/joins/peer-1/refuse",
	} {
		if rec := b.do("POST", path, `{}`, nil); rec.Code != 404 {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
}
