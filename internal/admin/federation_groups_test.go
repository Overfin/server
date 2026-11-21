package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type groupState struct {
	Groups []struct {
		ID, Name, Role, Me, Owner string
		WaitDays, CapPerDay       int64
		Accept                    bool
		Offers                    int
		Pool                      limitsJSON
		Members                   []struct {
			ID, Name, Role, InvitedBy string
			Paired, Me                bool
		}
		Links []struct {
			Peer, Name string
			Only       bool
		}
	}
	Peers []struct {
		ID, Name, State string
		Groups          []string
		ByHand          bool
	}
	Codes []struct {
		ID, Group string
		Uses      int
	}
}

func readGroups(t *testing.T, b *browser) groupState {
	t.Helper()
	rec := b.do("GET", "/admin/api/federation", "", nil)
	var s groupState
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &s) != nil {
		t.Fatalf("state: %d %s", rec.Code, rec.Body)
	}
	return s
}

// waitFor reads b's state until ok says so, for at most 10 seconds: the
// engines pass the group's events in the background.
func waitFor(t *testing.T, b *browser, what string, ok func(groupState) bool) groupState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s := readGroups(t, b)
		if ok(s) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %+v", what, s)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// From the panels: A founds a group and makes a code for it; B reads it
// (the group's name shown), uses it, and A accepts; B is then a member.
// A names B admin, changes the settings and what it offers the group;
// B leaves, keeping its pairing with A.
func TestFederationGroupsPanel(t *testing.T) {
	a, _, _ := fedPanel(t, "A")
	b, _, _ := fedPanel(t, "B")
	if rec := a.do("POST", "/admin/api/federation/groups", `{"name":"  ","waitDays":3,"capPerDay":5}`, nil); rec.Code != 400 {
		t.Fatalf("no name: %d %s", rec.Code, rec.Body)
	}
	rec := a.do("POST", "/admin/api/federation/groups", `{"name":"Family","waitDays":3,"capPerDay":5}`, nil)
	var g struct{ ID, Role string }
	json.Unmarshal(rec.Body.Bytes(), &g)
	if rec.Code != 201 || g.Role != "owner" {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("POST", "/admin/api/federation/codes", `{"note":"for B","group":"`+g.ID+`","uses":101}`, nil); rec.Code != 400 {
		t.Fatalf("101 uses: %d %s", rec.Code, rec.Body)
	}
	rec = a.do("POST", "/admin/api/federation/codes", `{"note":"for B","group":"`+g.ID+`","uses":2,"hours":48}`, nil)
	var made struct{ Code string }
	json.Unmarshal(rec.Body.Bytes(), &made)
	if rec.Code != 201 {
		t.Fatalf("group code: %d %s", rec.Code, rec.Body)
	}
	if s := readGroups(t, a); len(s.Codes) != 1 || s.Codes[0].Group != g.ID || s.Codes[0].Uses != 2 {
		t.Fatalf("A's codes: %+v", s.Codes)
	}
	body, _ := json.Marshal(map[string]string{"code": made.Code})
	rec = b.do("POST", "/admin/api/federation/preview", string(body), nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"groupName":"Family"`) {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do("POST", "/admin/api/federation/peers", string(body), nil); rec.Code != 201 {
		t.Fatalf("use: %d %s", rec.Code, rec.Body)
	}
	sa := readGroups(t, a)
	if len(sa.Peers) != 1 || sa.Peers[0].State != "ours" {
		t.Fatalf("A's peers: %+v", sa.Peers)
	}
	if rec := a.do("POST", "/admin/api/federation/peers/"+sa.Peers[0].ID+"/accept", "{}", nil); rec.Code != 200 {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body)
	}
	sb := waitFor(t, b, "B joins", func(s groupState) bool { return len(s.Groups) == 1 && len(s.Groups[0].Members) == 2 })
	bg := sb.Groups[0]
	if bg.Name != "Family" || bg.Role != "member" || bg.ID != g.ID {
		t.Fatalf("B's group: %+v", bg)
	}
	var bID string
	for _, m := range bg.Members {
		if m.Me {
			bID = m.ID
			if m.InvitedBy != "A" {
				t.Errorf("B invited by %q", m.InvitedBy)
			}
		}
	}

	// B, a member, can't change the settings.
	if rec := b.do("PUT", "/admin/api/federation/groups/"+g.ID, `{"name":"Mine","waitDays":1,"capPerDay":1}`, nil); rec.Code != 403 {
		t.Fatalf("B changing the settings: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("PUT", "/admin/api/federation/groups/"+g.ID+"/admins/"+bID, `{"on":true}`, nil); rec.Code != 204 {
		t.Fatalf("admin: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("PUT", "/admin/api/federation/groups/"+g.ID, `{"name":"Friends","waitDays":0,"capPerDay":10}`, nil); rec.Code != 204 {
		t.Fatalf("settings: %d %s", rec.Code, rec.Body)
	}
	waitFor(t, b, "B sees its role and the settings", func(s groupState) bool {
		return len(s.Groups) == 1 && s.Groups[0].Role == "admin" && s.Groups[0].Name == "Friends" && s.Groups[0].CapPerDay == 10
	})

	// What A offers the group, and its pool.
	rec = a.do("GET", "/admin/api/federation/groups/"+g.ID+"/sharing", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ours":[`) {
		t.Fatalf("sharing: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("PUT", "/admin/api/federation/groups/"+g.ID+"/sharing", `{"offer":[],"limits":{"streams":4,"transcodes":2,"bitrateKbps":0}}`, nil); rec.Code != 204 {
		t.Fatalf("set sharing: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("PUT", "/admin/api/federation/groups/"+g.ID+"/sharing", `{"offer":[],"limits":{"streams":1,"transcodes":2,"bitrateKbps":0}}`, nil); rec.Code != 400 {
		t.Fatalf("more transcodes than streams: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("PUT", "/admin/api/federation/groups/"+g.ID+"/answer", `{"accept":true}`, nil); rec.Code != 204 {
		t.Fatalf("answer: %d %s", rec.Code, rec.Body)
	}
	sa = readGroups(t, a)
	if ag := sa.Groups[0]; ag.Pool.Streams != 4 || !ag.Accept || len(ag.Links) != 1 || ag.Links[0].Name != "B" {
		t.Fatalf("A's group: %+v", ag)
	}

	// B leaves, ending nothing: its pairing with A stays, by hand.
	if rec := b.do("POST", "/admin/api/federation/groups/"+g.ID+"/leave", `{"end":[]}`, nil); rec.Code != 204 {
		t.Fatalf("leave: %d %s", rec.Code, rec.Body)
	}
	sb = readGroups(t, b)
	if len(sb.Groups) != 0 || len(sb.Peers) != 1 || sb.Peers[0].State != "paired" || !sb.Peers[0].ByHand || len(sb.Peers[0].Groups) != 0 {
		t.Fatalf("B after leaving: %+v", sb)
	}
	waitFor(t, a, "A sees B leave", func(s groupState) bool { return len(s.Groups) == 1 && len(s.Groups[0].Members) == 1 })
	if rec := a.do("POST", "/admin/api/federation/groups/nope/dissolve", `{"keepPairings":true}`, nil); rec.Code != 404 {
		t.Fatalf("no such group: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("POST", "/admin/api/federation/groups/"+g.ID+"/dissolve", `{"keepPairings":true}`, nil); rec.Code != 204 {
		t.Fatalf("dissolve: %d %s", rec.Code, rec.Body)
	}
	waitFor(t, a, "A dissolved", func(s groupState) bool { return len(s.Groups) == 0 })
}

// A server already paired with A uses A's group code: it waits under the
// group's joins, A accepts it from the panel, and it's a member.
func TestFederationGroupJoinWhenPaired(t *testing.T) {
	a, _, _ := fedPanel(t, "A")
	b, _, _ := fedPanel(t, "B")
	// Paired by hand first.
	rec := a.do("POST", "/admin/api/federation/codes", `{"note":"for B"}`, nil)
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
	waitFor(t, b, "B paired", func(s groupState) bool { return len(s.Peers) == 1 && s.Peers[0].State == "paired" })
	rec = a.do("POST", "/admin/api/federation/groups", `{"name":"Family","waitDays":3,"capPerDay":5}`, nil)
	var g struct{ ID string }
	json.Unmarshal(rec.Body.Bytes(), &g)
	rec = a.do("POST", "/admin/api/federation/codes", `{"note":"for B","group":"`+g.ID+`"}`, nil)
	json.Unmarshal(rec.Body.Bytes(), &made)
	body, _ = json.Marshal(map[string]string{"code": made.Code})
	if rec := b.do("POST", "/admin/api/federation/preview", string(body), nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"groupName":"Family"`) {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do("POST", "/admin/api/federation/peers", string(body), nil); rec.Code != 201 {
		t.Fatalf("join: %d %s", rec.Code, rec.Body)
	}
	rec = a.do("GET", "/admin/api/federation", "", nil)
	var st struct {
		Groups []struct {
			Joins []struct{ Peer, Name string }
		}
	}
	json.Unmarshal(rec.Body.Bytes(), &st)
	if len(st.Groups) != 1 || len(st.Groups[0].Joins) != 1 || st.Groups[0].Joins[0].Name != "B" {
		t.Fatalf("A's joins: %s", rec.Body)
	}
	if rec := a.do("POST", "/admin/api/federation/groups/"+g.ID+"/joins/"+st.Groups[0].Joins[0].Peer+"/accept", "{}", nil); rec.Code != 204 {
		t.Fatalf("accept join: %d %s", rec.Code, rec.Body)
	}
	waitFor(t, b, "B a member", func(s groupState) bool {
		return len(s.Groups) == 1 && len(s.Groups[0].Members) == 2 && s.Groups[0].Role == "member"
	})
}
