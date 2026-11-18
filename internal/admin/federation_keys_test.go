package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The panel's keys: the next key's fingerprint, exported (shown once
// asked, with the same fingerprint), deleted from the server, then given
// back to change the key; a change under way said; revoking with the next
// key gone and none given ends every pairing.
func TestFederationKeysPanel(t *testing.T) {
	ctx := context.Background()
	a, fa := fedPanel(t, "A")
	b, fb := fedPanel(t, "B")
	code, _, _ := fa.NewCode(ctx, "")
	if _, err := fb.UseCode(ctx, code); err != nil {
		t.Fatal(err)
	}
	peers, _ := fa.Store.Peers(ctx)
	if _, err := fa.Accept(ctx, peers[0].ID); err != nil {
		t.Fatal(err)
	}
	type keyState struct {
		Fingerprint string
		NextKey     struct {
			Fingerprint string
			OnServer    bool
		}
		Rotation *struct{ Since, Until string }
		Peers    []struct {
			ID, Commitment, OurKey string
		}
	}
	read := func(br *browser) keyState {
		t.Helper()
		var s keyState
		json.Unmarshal(br.do("GET", "/admin/api/federation", "", nil).Body.Bytes(), &s)
		return s
	}
	sa := read(a)
	if !sa.NextKey.OnServer || sa.NextKey.Fingerprint == "" || sa.Rotation != nil || sa.Peers[0].Commitment != "pair" {
		t.Fatalf("A: %+v", sa)
	}
	rec := a.do("POST", "/admin/api/federation/next-key/export", "{}", nil)
	var exported struct{ Text, Fingerprint string }
	json.Unmarshal(rec.Body.Bytes(), &exported)
	if rec.Code != 200 || !strings.HasPrefix(exported.Text, "overfin-next-key-1.") || exported.Fingerprint != sa.NextKey.Fingerprint {
		t.Fatalf("export: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(a.do("GET", "/admin/api/federation", "", nil).Body.String(), exported.Text) {
		t.Fatal("the next key's seed is in the page's state")
	}
	if rec := a.do("DELETE", "/admin/api/federation/next-key", "", nil); rec.Code != 204 {
		t.Fatalf("delete: %d", rec.Code)
	}
	if s := read(a); s.NextKey.OnServer || s.NextKey.Fingerprint != sa.NextKey.Fingerprint {
		t.Fatalf("after deleting: %+v", s.NextKey)
	}
	if rec := a.do("POST", "/admin/api/federation/next-key/export", "{}", nil); rec.Code != 409 {
		t.Fatalf("export after deleting: %d", rec.Code)
	}
	// Changing the key needs it back now.
	if rec := a.do("POST", "/admin/api/federation/key/rotate", `{}`, nil); rec.Code != 400 || !strings.Contains(rec.Body.String(), "paste the copy") {
		t.Fatalf("rotate without the key: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("POST", "/admin/api/federation/key/rotate", `{"nextKey":"overfin-next-key-1.nope"}`, nil); rec.Code != 400 || !strings.Contains(rec.Body.String(), "isn't this server's next key") {
		t.Fatalf("rotate with another: %d %s", rec.Code, rec.Body)
	}
	body, _ := json.Marshal(map[string]string{"nextKey": exported.Text})
	if rec := a.do("POST", "/admin/api/federation/key/rotate", string(body), nil); rec.Code != 204 {
		t.Fatalf("rotate: %d %s", rec.Code, rec.Body)
	}
	s := read(a)
	if s.Fingerprint != sa.NextKey.Fingerprint || !s.NextKey.OnServer || s.NextKey.Fingerprint == sa.NextKey.Fingerprint {
		t.Fatalf("after the change: %+v", s)
	}
	// B moved: no change under way, B's view says the commitment came
	// with the rotation.
	if s.Rotation != nil || s.Peers[0].OurKey != "" {
		t.Fatalf("A after B moved: %+v", s)
	}
	if sb := read(b); sb.Peers[0].Commitment != "rotation" {
		t.Fatalf("B: %+v", sb)
	}
	// Stolen, without the next key: revoked, every pairing ends.
	a.do("DELETE", "/admin/api/federation/next-key", "", nil)
	rec = a.do("POST", "/admin/api/federation/key/revoke", `{}`, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"rotated":false`) {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	if s := read(a); len(s.Peers) != 0 || s.Fingerprint == sa.NextKey.Fingerprint || !s.NextKey.OnServer {
		t.Fatalf("after revoking: %+v", s)
	}
	var sb struct {
		Peers []struct{ SuspendedAt string }
	}
	json.Unmarshal(b.do("GET", "/admin/api/federation", "", nil).Body.Bytes(), &sb)
	if len(sb.Peers) != 1 || sb.Peers[0].SuspendedAt == "" {
		t.Fatalf("B after A revoked: %+v", sb)
	}
}
