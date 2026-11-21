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
	"time"

	"github.com/msfp-io/msfp-go"
	"github.com/msfp-io/msfp-go/wire"
	"github.com/overfin/server/internal/store"
)

// fedPanel is a signed-in panel whose server has federation, the MSFP
// engine, on a test server of its own: over HTTPS, as MSFP is, with the
// test server's certificate, which the server names to its peers.
func fedPanel(t *testing.T, name string) (*browser, *msfp.Engine, *store.Store) {
	t.Helper()
	_, b, e, db := fedPanelOf(t, name)
	return b, e, db
}

// fedPanelOf is fedPanel with the panel itself, to set its fields.
func fedPanelOf(t *testing.T, name string) (*Panel, *browser, *msfp.Engine, *store.Store) {
	t.Helper()
	p, b, db, _ := signedInPanel(t)
	mux := http.NewServeMux()
	ts := httptest.NewTLSServer(mux)
	t.Cleanup(ts.Close)
	e, err := msfp.Open(context.Background(), msfp.Config{Address: ts.URL, Store: db.MSFP(), Name: func() string { return name },
		Pins: []string{wire.Pin(ts.Certificate())}, AllowLoopback: true, Clock: secondsClock{},
		Parts: []string{"catalog", "playback", "media", "video", "groups"}, Catalog: store.MSFPCatalog{S: db},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/api/v1/federation/", e.FederationHandler())
	p.Engine = func() *msfp.Engine { return e }
	return p, b, e, db
}

// secondsClock is the system's time cut to the second: an engine started
// at it hears requests dated the second it started, which tests make at
// once.
type secondsClock struct{}

func (secondsClock) Now() time.Time { return time.Now().Truncate(time.Second) }

// putPeer writes a peer as the engine would have.
func putPeer(t *testing.T, db *store.Store, p msfp.PeerRecord) {
	t.Helper()
	if p.Key == nil {
		p.Key = wire.NewKey().Public()
	}
	if p.Inbound == "" {
		p.Inbound = msfp.InboundYes
	}
	if err := db.MSFP().Update(context.Background(), func(tx msfp.Tx) error { return tx.PutPeer(p) }); err != nil {
		t.Fatal(err)
	}
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
	a, _, _ := fedPanel(t, "A")
	b, _, _ := fedPanel(t, "B")
	rec := a.do("POST", "/admin/api/federation/codes", `{"note":"  for   B "}`, nil)
	var made struct{ ID, Note, Code string }
	json.Unmarshal(rec.Body.Bytes(), &made)
	if rec.Code != 201 || !strings.HasPrefix(made.Code, "msfp-pair-0.") || made.Note != "for B" {
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
	a, fa, dbA := fedPanel(t, "A")
	b, fb, _ := fedPanel(t, "B")
	dbA.SetLibraries(ctx, []store.Library{{ID: "films", Item: json.RawMessage(`{"Name":"Films","CollectionType":"movies"}`)},
		{ID: "series", Item: json.RawMessage(`{"Name":"Séries","CollectionType":"tvshows"}`)}})
	code, _, _ := fa.MakeCode(ctx, "")
	aOnB, err := fb.RedeemCode(ctx, code)
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
	if rec := a.do("PUT", "/admin/api/federation/peers/"+bOnA.ID+"/sharing", `{"offer":["films"],"want":[],"limits":{"streams":1,"transcodes":2,"bitrateKbps":0}}`, nil); rec.Code != 400 {
		t.Fatalf("more transcodes than streams: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("PUT", "/admin/api/federation/peers/"+bOnA.ID+"/sharing", `{"offer":["films"],"want":[],"limits":{"streams":4,"transcodes":2,"bitrateKbps":12000}}`, nil); rec.Code != 204 {
		t.Fatalf("A offers: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("GET", "/admin/api/federation/peers/"+bOnA.ID+"/sharing", "", nil); !strings.Contains(rec.Body.String(), `"limits":{"streams":4,"transcodes":2,"bitrateKbps":12000}`) {
		t.Fatalf("A's limits: %s", rec.Body)
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
	// By the ID A gives it (never Jellyfin's "films"), and one A doesn't
	// offer.
	if rec := b.do("PUT", "/admin/api/federation/peers/"+aOnB.ID+"/sharing", `{"offer":[],"want":["`+sh.Theirs[0].ID+`","series"]}`, nil); rec.Code != 204 {
		t.Fatalf("B wants: %d %s", rec.Code, rec.Body)
	}
	if s := readState(t, b); s.Peers[0].Wants != 1 {
		t.Fatalf("B wants what A doesn't offer: %+v", s.Peers)
	}
	if s := readState(t, a); s.Peers[0].Offers != 1 {
		t.Fatalf("A's count: %+v", s.Peers)
	}
	// Check answers with the same counts the list shows.
	rec = b.do("POST", "/admin/api/federation/peers/"+aOnB.ID+"/check", "{}", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"wants":1`) {
		t.Fatalf("check: %d %s", rec.Code, rec.Body)
	}
}

// A library we want that the peer no longer offers (it changed its
// library IDs, say) is counted on the peer's row, to choose again.
func TestFederationPanelWantsGone(t *testing.T) {
	a, _, db := fedPanel(t, "A")
	putPeer(t, db, msfp.PeerRecord{ID: "pb", Name: "B", Address: "https://b.example.org", State: msfp.Paired,
		Wants: []msfp.WantedLibrary{{ID: "old", Name: "Films", Kind: "movies", Wanted: true, Gone: true},
			{ID: "lib_new", Name: "Films", Kind: "movies"}}})
	var st struct {
		Peers []struct{ WantsGone int }
	}
	rec := a.do("GET", "/admin/api/federation", "", nil)
	if json.Unmarshal(rec.Body.Bytes(), &st) != nil || len(st.Peers) != 1 || st.Peers[0].WantsGone != 1 {
		t.Fatalf("state: %d %s", rec.Code, rec.Body)
	}
}

// fakeUncallable is the gateway's setting without an engine: kept until
// it opens one.
type fakeUncallable struct{ on *bool }

func (f fakeUncallable) FederationUncallable(context.Context) (bool, error) { return *f.on, nil }

func (f fakeUncallable) SetFederationUncallable(_ context.Context, on bool) error {
	*f.on = on
	return nil
}

// "This server can't be called" (MSFP core section 7.5): shown, and set,
// even without the site's address, which it doesn't need; a server that
// can't be called makes no code, and a peer that can't be called is
// listed as such, with callsFailed.
func TestFederationPanelUncallable(t *testing.T) {
	p, b, _, _ := signedInPanel(t)
	p.Uncallable = fakeUncallable{new(bool)}
	read := func() map[string]any {
		t.Helper()
		rec := b.do("GET", "/admin/api/federation", "", nil)
		var s map[string]any
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &s) != nil {
			t.Fatalf("state: %d %s", rec.Code, rec.Body)
		}
		return s
	}
	if s := read(); s["available"] != false || s["uncallable"] != false || s["canSet"] != true {
		t.Fatalf("at first: %v", s)
	}
	if rec := b.do("PUT", "/admin/api/federation/uncallable", `{"on":true}`, nil); rec.Code != 204 {
		t.Fatalf("turning it on: %d %s", rec.Code, rec.Body)
	}
	if s := read(); s["uncallable"] != true {
		t.Fatalf("on: %v", s)
	}

	a, e, db := fedPanel(t, "A")
	if err := e.SetUncallable(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if rec := a.do("POST", "/admin/api/federation/codes", `{"note":""}`, nil); rec.Code != 409 || !strings.Contains(rec.Body.String(), "no-codes") {
		t.Fatalf("a code from a server that can't be called: %d %s", rec.Code, rec.Body)
	}
	putPeer(t, db, msfp.PeerRecord{ID: "pp", Name: "P", Address: wire.NewUncallableAddress(), State: msfp.Paired,
		Inbound: msfp.InboundNo, CallsFailed: true})
	rec := a.do("GET", "/admin/api/federation", "", nil)
	var st struct {
		Uncallable bool
		Peers      []struct {
			Inbound     string
			CallsFailed bool
		}
	}
	if json.Unmarshal(rec.Body.Bytes(), &st) != nil || !st.Uncallable || len(st.Peers) != 1 || st.Peers[0].Inbound != "no" || !st.Peers[0].CallsFailed {
		t.Fatalf("the list: %s", rec.Body)
	}
}
