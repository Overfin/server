package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type groupState struct {
	Groups []struct {
		ID, Name, Role, Me, Owner string
		WaitDays, CapPerDay       int64
		Accept, NoWayIn           bool
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
		Individual      bool
		Asked           bool
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

	// B leaves, asking A to stay paired: the pairing waits for A's answer.
	aOnB := readGroups(t, b).Peers[0].ID
	if rec := b.do("POST", "/admin/api/federation/groups/"+g.ID+"/leave", `{"ask":["`+aOnB+`"]}`, nil); rec.Code != 204 {
		t.Fatalf("leave: %d %s", rec.Code, rec.Body)
	}
	sb = readGroups(t, b)
	if len(sb.Groups) != 0 || len(sb.Peers) != 1 || sb.Peers[0].State != "paired" || !sb.Peers[0].Asked || len(sb.Peers[0].Groups) != 0 {
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

// fakeCodeMail keeps the codes the panel emails; fail, set, refuses.
type fakeCodeMail struct {
	to, code, group string
	fail            error
}

func (f *fakeCodeMail) CanSendMail() bool { return true }

func (f *fakeCodeMail) SendPairingCode(_ context.Context, to, code, group string, _ time.Time) error {
	if f.fail != nil {
		return f.fail
	}
	f.to, f.code, f.group = to, code, group
	return nil
}

// A group's code emailed from the panel: sent with the group's name; a
// failure is said beside the code, which is shown anyway; a bad address is
// refused before any code is made.
func TestFederationCodeByEmail(t *testing.T) {
	p, a, _, _ := fedPanelOf(t, "A")
	mailer := &fakeCodeMail{}
	p.CodeMail = mailer
	if !strings.Contains(a.do("GET", "/admin/api/federation", "", nil).Body.String(), `"canMail":true`) {
		t.Fatal("canMail isn't said")
	}
	rec := a.do("POST", "/admin/api/federation/groups", `{"name":"Family","waitDays":3,"capPerDay":5}`, nil)
	var g struct{ ID string }
	json.Unmarshal(rec.Body.Bytes(), &g)
	if rec := a.do("POST", "/admin/api/federation/codes", `{"note":"x","email":"not an address"}`, nil); rec.Code != 400 {
		t.Fatalf("bad address: %d %s", rec.Code, rec.Body)
	}
	if s := readGroups(t, a); len(s.Codes) != 0 {
		t.Fatalf("a code was made for a bad address: %+v", s.Codes)
	}
	rec = a.do("POST", "/admin/api/federation/codes", `{"note":"x","group":"`+g.ID+`","email":"op@example.org"}`, nil)
	var made struct{ Code, MailedTo, MailProblem string }
	json.Unmarshal(rec.Body.Bytes(), &made)
	if rec.Code != 201 || made.MailedTo != "op@example.org" || mailer.code != made.Code || mailer.group != "Family" || mailer.to != "op@example.org" {
		t.Fatalf("emailed: %d %s (sent %+v)", rec.Code, rec.Body, mailer)
	}
	mailer.fail = errors.New("It didn't go through: the server said no.")
	rec = a.do("POST", "/admin/api/federation/codes", `{"note":"x","email":"op@example.org"}`, nil)
	made.MailedTo, made.MailProblem = "", ""
	json.Unmarshal(rec.Body.Bytes(), &made)
	if rec.Code != 201 || made.Code == "" || made.MailedTo != "" || !strings.Contains(made.MailProblem, "didn't go through") {
		t.Fatalf("failed: %d %s", rec.Code, rec.Body)
	}
}

// No way in (MSFP groups section 6): A's pairing with B is A's only one
// through the group. The panel lists the group before it ends; once it
// has, the group says so, and asking B is refused with why.
func TestFederationGroupNoWayIn(t *testing.T) {
	a, _, _ := fedPanel(t, "A")
	b, _, _ := fedPanel(t, "B")
	rec := a.do("POST", "/admin/api/federation/groups", `{"name":"Family","waitDays":3,"capPerDay":5}`, nil)
	var g struct{ ID string }
	json.Unmarshal(rec.Body.Bytes(), &g)
	rec = a.do("POST", "/admin/api/federation/codes", `{"group":"`+g.ID+`","uses":1}`, nil)
	var made struct{ Code string }
	json.Unmarshal(rec.Body.Bytes(), &made)
	body, _ := json.Marshal(map[string]string{"code": made.Code})
	if rec := b.do("POST", "/admin/api/federation/peers", string(body), nil); rec.Code != 201 {
		t.Fatalf("use: %d %s", rec.Code, rec.Body)
	}
	peer := readGroups(t, a).Peers[0].ID
	if rec := a.do("POST", "/admin/api/federation/peers/"+peer+"/accept", "{}", nil); rec.Code != 200 {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body)
	}
	waitFor(t, b, "B joins", func(s groupState) bool { return len(s.Groups) == 1 && len(s.Groups[0].Members) == 2 })
	sa := waitFor(t, a, "A has B", func(s groupState) bool { return len(s.Groups) == 1 && len(s.Groups[0].Members) == 2 })
	if sa.Groups[0].NoWayIn {
		t.Fatal("no way in before the end")
	}
	rec = a.do("GET", "/admin/api/federation/peers/"+peer+"/cuts", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"Family"`) || !strings.Contains(rec.Body.String(), g.ID) {
		t.Fatalf("cuts: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("DELETE", "/admin/api/federation/peers/"+peer, "", nil); rec.Code != 204 {
		t.Fatalf("end: %d %s", rec.Code, rec.Body)
	}
	sa = readGroups(t, a)
	if !sa.Groups[0].NoWayIn {
		t.Fatalf("A after the end: %+v", sa.Groups[0])
	}
	var bID string
	for _, m := range sa.Groups[0].Members {
		if !m.Me {
			bID = m.ID
		}
	}
	rec = a.do("POST", "/admin/api/federation/groups/"+g.ID+"/asks/"+bID, "{}", nil)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"type":"no-way-in"`) {
		t.Fatalf("asking B: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("GET", "/admin/api/federation/peers/"+peer+"/cuts", "", nil); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("cuts after: %d %s", rec.Code, rec.Body)
	}
}

// Relations from the panels (MSFP core section 7.7): B joins A's group by
// its code, paired through the group only; B asks to pair directly, A
// accepts; B unpairs, and the group keeps them paired.
func TestFederationRelations(t *testing.T) {
	a, _, _ := fedPanel(t, "A")
	b, _, _ := fedPanel(t, "B")
	rec := a.do("POST", "/admin/api/federation/groups", `{"name":"Family","waitDays":3,"capPerDay":5}`, nil)
	var g struct{ ID string }
	json.Unmarshal(rec.Body.Bytes(), &g)
	rec = a.do("POST", "/admin/api/federation/codes", `{"group":"`+g.ID+`","uses":1}`, nil)
	var made struct{ Code string }
	json.Unmarshal(rec.Body.Bytes(), &made)
	body, _ := json.Marshal(map[string]string{"code": made.Code})
	if rec := b.do("POST", "/admin/api/federation/peers", string(body), nil); rec.Code != 201 {
		t.Fatalf("use: %d %s", rec.Code, rec.Body)
	}
	bOnA := readGroups(t, a).Peers[0].ID
	if rec := a.do("POST", "/admin/api/federation/peers/"+bOnA+"/accept", "{}", nil); rec.Code != 200 {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body)
	}
	sb := waitFor(t, b, "B joins", func(s groupState) bool {
		return len(s.Groups) == 1 && len(s.Groups[0].Members) == 2 && len(s.Peers) == 1 && s.Peers[0].State == "paired"
	})
	if p := sb.Peers[0]; p.Individual || len(p.Groups) != 1 {
		t.Fatalf("B's A, through the group only: %+v", p)
	}
	aOnB := sb.Peers[0].ID
	if rec := b.do("POST", "/admin/api/federation/peers/"+aOnB+"/individual", "{}", nil); rec.Code != 204 {
		t.Fatalf("B asks: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("POST", "/admin/api/federation/peers/"+bOnA+"/individual", "{}", nil); rec.Code != 204 {
		t.Fatalf("A accepts: %d %s", rec.Code, rec.Body)
	}
	waitFor(t, b, "B paired directly", func(s groupState) bool { return len(s.Peers) == 1 && s.Peers[0].Individual })
	if rec := b.do("POST", "/admin/api/federation/peers/"+aOnB+"/unpair", "{}", nil); rec.Code != 204 {
		t.Fatalf("B unpairs: %d %s", rec.Code, rec.Body)
	}
	waitFor(t, a, "A follows, the group keeps them", func(s groupState) bool {
		return len(s.Peers) == 1 && !s.Peers[0].Individual && len(s.Peers[0].Groups) == 1
	})
	if rec := a.do("POST", "/admin/api/federation/peers/nope/block", "{}", nil); rec.Code != 404 {
		t.Fatalf("block nobody: %d %s", rec.Code, rec.Body)
	}
}
