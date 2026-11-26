package admin

import (
	"encoding/json"
	"testing"
)

// joinedGroup: A founds "Family", B joins with a code A makes and A
// accepts; it gives the group's ID and B's member ID.
func joinedGroup(t *testing.T, a, b *browser) (group, bID string) {
	t.Helper()
	rec := a.do("POST", "/admin/api/federation/groups", `{"name":"Family","waitDays":0,"capPerDay":5}`, nil)
	var g struct{ ID string }
	json.Unmarshal(rec.Body.Bytes(), &g)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	rec = a.do("POST", "/admin/api/federation/codes", `{"note":"for B","group":"`+g.ID+`","uses":1}`, nil)
	var made struct{ Code string }
	json.Unmarshal(rec.Body.Bytes(), &made)
	body, _ := json.Marshal(map[string]string{"code": made.Code})
	if rec := b.do("POST", "/admin/api/federation/peers", string(body), nil); rec.Code != 201 {
		t.Fatalf("use: %d %s", rec.Code, rec.Body)
	}
	sa := readGroups(t, a)
	if rec := a.do("POST", "/admin/api/federation/peers/"+sa.Peers[0].ID+"/accept", "{}", nil); rec.Code != 200 {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body)
	}
	sb := waitFor(t, b, "B joins", func(s groupState) bool { return len(s.Groups) == 1 && len(s.Groups[0].Members) == 2 })
	for _, m := range sb.Groups[0].Members {
		if m.Me {
			bID = m.ID
		}
	}
	waitFor(t, a, "A sees B", func(s groupState) bool { return len(s.Groups) == 1 && len(s.Groups[0].Members) == 2 })
	return g.ID, bID
}

// From the panel: the heir named; a ban, by the owner, of a member, then
// lifted; members can't ban; the group given away, after which the old
// owner can't ban either; malformed requests and unknown groups or
// members refused with the right status.
func TestFederationGroupModeration(t *testing.T) {
	a, _, _ := fedPanel(t, "A")
	b, _, _ := fedPanel(t, "B")
	g, bID := joinedGroup(t, a, b)
	base := "/admin/api/federation/groups/" + g

	// Malformed bodies.
	for _, path := range []string{"/heir", "/give", "/bans", "/blocks"} {
		method := "POST"
		if path == "/heir" {
			method = "PUT"
		}
		if rec := a.do(method, base+path, `{"member":`, nil); rec.Code != 400 {
			t.Errorf("%s malformed: %d %s", path, rec.Code, rec.Body)
		}
	}
	// A member can't ban; nor ask anything of a group that isn't.
	if rec := b.do("POST", base+"/bans", `{"member":"`+bID+`","reason":"x"}`, nil); rec.Code != 403 {
		t.Errorf("B banning: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("POST", "/admin/api/federation/groups/nope/bans", `{"member":"`+bID+`","reason":"x"}`, nil); rec.Code != 404 {
		t.Errorf("ban in no group: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("DELETE", base+"/refusals/nobody", "", nil); rec.Code >= 500 {
		t.Errorf("lifting no refusal: %d %s", rec.Code, rec.Body)
	}

	if rec := a.do("PUT", base+"/heir", `{"member":"`+bID+`"}`, nil); rec.Code != 204 {
		t.Fatalf("heir: %d %s", rec.Code, rec.Body)
	}
	// Banned: B is out, on both sides.
	if rec := a.do("POST", base+"/bans", `{"member":"`+bID+`","reason":"spam"}`, nil); rec.Code != 204 {
		t.Fatalf("ban: %d %s", rec.Code, rec.Body)
	}
	waitFor(t, a, "B out of A's group", func(s groupState) bool { return len(s.Groups) == 1 && len(s.Groups[0].Members) == 1 })
	waitFor(t, b, "B out of the group", func(s groupState) bool { return len(s.Groups) == 0 })
	if rec := a.do("DELETE", base+"/bans/"+bID, "", nil); rec.Code != 204 {
		t.Errorf("unban: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("DELETE", base+"/bans/"+bID, "", nil); rec.Code == 500 {
		t.Errorf("unban twice: %d %s", rec.Code, rec.Body)
	}
}

// The group given away: the new owner owns it, the old one is an admin
// and may no longer do what only the owner does.
func TestFederationGroupGiven(t *testing.T) {
	a, _, _ := fedPanel(t, "A")
	b, _, _ := fedPanel(t, "B")
	g, bID := joinedGroup(t, a, b)
	base := "/admin/api/federation/groups/" + g
	if rec := a.do("POST", base+"/give", `{"member":"nobody"}`, nil); rec.Code < 400 || rec.Code == 500 {
		t.Errorf("give to no member: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("POST", base+"/give", `{"member":"`+bID+`"}`, nil); rec.Code != 204 {
		t.Fatalf("give: %d %s", rec.Code, rec.Body)
	}
	waitFor(t, b, "B owns it", func(s groupState) bool { return len(s.Groups) == 1 && s.Groups[0].Role == "owner" })
	sa := waitFor(t, a, "A doesn't", func(s groupState) bool { return len(s.Groups) == 1 && s.Groups[0].Role != "owner" })
	var aID string
	for _, m := range sa.Groups[0].Members {
		if m.Me {
			aID = m.ID
		}
	}
	if rec := a.do("POST", base+"/give", `{"member":"`+aID+`"}`, nil); rec.Code != 403 {
		t.Errorf("A giving it back: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("PUT", base+"/heir", `{"member":"`+aID+`"}`, nil); rec.Code != 403 {
		t.Errorf("A naming the heir: %d %s", rec.Code, rec.Body)
	}
}
