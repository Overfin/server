package admin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/overfin/server/internal/federation"
	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/store"
)

// fedPanel is a signed-in panel whose server has federation, on a test
// server of its own.
func fedPanel(t *testing.T, name string) (*browser, *federation.Service) {
	t.Helper()
	p, b, db, _ := signedInPanel(t)
	key, err := federation.LoadKey(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	s := &federation.Service{Store: db, Key: key, Address: ts.URL, HTTP: federation.NewHTTPClient(),
		Name: func(context.Context) string { return name }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := plugin.NewRegistrar(mux, func(h http.Handler) http.Handler { return h }).Load(federation.Plugin{S: s}); err != nil {
		t.Fatal(err)
	}
	p.Federation = func() *federation.Service { return s }
	return b, s
}

type fedState struct {
	Available   bool
	Fingerprint string
	Codes       []struct{ ID, Note string }
	Peers       []struct {
		ID, Name, State, Fingerprint string
		Offers, Wants                int
	}
}

func readState(t *testing.T, b *browser) fedState {
	t.Helper()
	rec := b.do("GET", "/admin/api/federation", "", nil)
	var s fedState
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &s) != nil {
		t.Fatalf("state: %d %s", rec.Code, rec.Body)
	}
	return s
}

// Two operators pair their servers from their panels: one makes a code,
// the other reads it (the fingerprint matches the first server's), uses
// it, and the first accepts; then one ends it.
func TestFederationPanel(t *testing.T) {
	a, _ := fedPanel(t, "A")
	b, _ := fedPanel(t, "B")
	rec := a.do("POST", "/admin/api/federation/codes", `{"note":"  for   B "}`, nil)
	var made struct{ ID, Note, Code string }
	json.Unmarshal(rec.Body.Bytes(), &made)
	if rec.Code != 201 || !strings.HasPrefix(made.Code, "overfin-pair-1.") || made.Note != "for B" {
		t.Fatalf("code: %d %s", rec.Code, rec.Body)
	}
	sa := readState(t, a)
	if len(sa.Codes) != 1 || strings.Contains(a.do("GET", "/admin/api/federation", "", nil).Body.String(), made.Code) {
		t.Fatalf("codes listed: %+v (the code itself must not be)", sa.Codes)
	}
	// Reading it on B: A's fingerprint.
	body, _ := json.Marshal(map[string]string{"code": made.Code})
	rec = b.do("POST", "/admin/api/federation/preview", string(body), nil)
	var preview struct {
		Fingerprint string
		Expired     bool
	}
	json.Unmarshal(rec.Body.Bytes(), &preview)
	if rec.Code != 200 || preview.Fingerprint != sa.Fingerprint || preview.Expired {
		t.Fatalf("preview: %d %s (A's fingerprint %s)", rec.Code, rec.Body, sa.Fingerprint)
	}
	// A reading its own code is told so.
	if rec := a.do("POST", "/admin/api/federation/preview", string(body), nil); rec.Code != 400 || !strings.Contains(rec.Body.String(), "own code") {
		t.Fatalf("own code: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do("POST", "/admin/api/federation/peers", `{"code":"nonsense"}`, nil); rec.Code != 400 || !strings.Contains(rec.Body.String(), "isn't a pairing code") {
		t.Fatalf("nonsense: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do("POST", "/admin/api/federation/peers", string(body), nil); rec.Code != 201 {
		t.Fatalf("use: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do("POST", "/admin/api/federation/peers", string(body), nil); rec.Code != 400 || !strings.Contains(rec.Body.String(), "already in the list") {
		t.Fatalf("used twice: %d %s", rec.Code, rec.Body)
	}
	sa = readState(t, a)
	sb := readState(t, b)
	if len(sa.Peers) != 1 || sa.Peers[0].State != "ours" || sa.Peers[0].Fingerprint != sb.Fingerprint || len(sa.Codes) != 0 {
		t.Fatalf("A: %+v (B's fingerprint %s)", sa, sb.Fingerprint)
	}
	if len(sb.Peers) != 1 || sb.Peers[0].State != "theirs" {
		t.Fatalf("B: %+v", sb)
	}
	if rec := a.do("POST", "/admin/api/federation/peers/"+sa.Peers[0].ID+"/accept", "{}", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"paired"`) {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body)
	}
	if s := readState(t, b); s.Peers[0].State != "paired" {
		t.Fatalf("B after A accepted: %+v", s.Peers)
	}
	if rec := b.do("POST", "/admin/api/federation/peers/"+sb.Peers[0].ID+"/check", "{}", nil); rec.Code != 200 || strings.Contains(rec.Body.String(), "unreachable") {
		t.Fatalf("check: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("DELETE", "/admin/api/federation/peers/"+sa.Peers[0].ID, "", nil); rec.Code != 204 {
		t.Fatalf("end: %d %s", rec.Code, rec.Body)
	}
	if s := readState(t, b); len(s.Peers) != 0 {
		t.Fatalf("B still lists A: %+v", s.Peers)
	}
	if rec := a.do("DELETE", "/admin/api/federation/peers/"+sa.Peers[0].ID, "", nil); rec.Code != 404 {
		t.Fatalf("ended twice: %d", rec.Code)
	}
}

// Without the site's address, the page says federation is unavailable,
// and nothing can be made.
func TestFederationUnavailable(t *testing.T) {
	_, b, _, _ := signedInPanel(t)
	if s := readState(t, b); s.Available {
		t.Fatalf("available without an address: %+v", s)
	}
	if rec := b.do("POST", "/admin/api/federation/codes", `{"note":""}`, nil); rec.Code != 409 {
		t.Fatalf("a code without federation: %d", rec.Code)
	}
}

// Sharing from the panels: A offers Films to B; B sees it and wants it;
// each list counts it; a server not paired yet has no sharing.
func TestSharingPanel(t *testing.T) {
	ctx := context.Background()
	a, fa := fedPanel(t, "A")
	b, fb := fedPanel(t, "B")
	fa.Store.SetLibraries(ctx, []store.Library{{ID: "films", Item: json.RawMessage(`{"Name":"Films","CollectionType":"movies"}`)},
		{ID: "series", Item: json.RawMessage(`{"Name":"Séries","CollectionType":"tvshows"}`)}})
	code, _, _ := fa.NewCode(ctx, "")
	aOnB, err := fb.UseCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	bOnA := readState(t, a).Peers[0]
	if rec := a.do("GET", "/admin/api/federation/peers/"+bOnA.ID+"/sharing", "", nil); rec.Code != 409 {
		t.Fatalf("sharing before pairing: %d", rec.Code)
	}
	fa.Accept(ctx, bOnA.ID)

	rec := a.do("GET", "/admin/api/federation/peers/"+bOnA.ID+"/sharing", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"Films","kind":"movies","picked":false`) {
		t.Fatalf("A's sharing: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("PUT", "/admin/api/federation/peers/"+bOnA.ID+"/sharing", `{"offer":["films"],"want":[]}`, nil); rec.Code != 204 {
		t.Fatalf("A offers: %d %s", rec.Code, rec.Body)
	}
	rec = b.do("GET", "/admin/api/federation/peers/"+aOnB.ID+"/sharing", "", nil)
	var sh struct {
		Theirs    []struct{ ID, Name string }
		Reachable bool
	}
	json.Unmarshal(rec.Body.Bytes(), &sh)
	if !sh.Reachable || len(sh.Theirs) != 1 || sh.Theirs[0].Name != "Films" {
		t.Fatalf("B sees: %s", rec.Body)
	}
	if rec := b.do("PUT", "/admin/api/federation/peers/"+aOnB.ID+"/sharing", `{"offer":[],"want":["films","series"]}`, nil); rec.Code != 204 {
		t.Fatalf("B wants: %d %s", rec.Code, rec.Body)
	}
	if s := readState(t, b); s.Peers[0].Wants != 1 {
		t.Fatalf("B wants what A doesn't offer: %+v", s.Peers)
	}
	if s := readState(t, a); s.Peers[0].Offers != 1 {
		t.Fatalf("A's count: %+v", s.Peers)
	}
}
