package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/overfin/server/internal/connector"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/provision"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

// signedInPanel is a panel with a database and a fake Jellyfin, and a
// browser already signed in to it.
func signedInPanel(t *testing.T) (*Panel, *browser, *store.Store, *[]string) {
	t.Helper()
	p, idp, _ := newPanel(t)
	box, _ := seal.New(bytes.Repeat([]byte{3}, 32))
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	var mu sync.Mutex
	var calls []string
	jfSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(jfSrv.Close)
	jf, _ := jellyfin.New(jfSrv.URL, "test")
	p.Store = db
	prov := &provision.Provisioner{Store: db, Jellyfin: jf, AdminKey: "admin", LogKey: []byte("k"),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	p.Provisioner = func() *provision.Provisioner { return prov }
	b := &browser{h: p.Handler(), cookies: map[string]string{}}
	if rec := signIn(t, b, idp, []string{"overfin-admins"}); rec.Code != http.StatusSeeOther {
		t.Fatalf("sign in: %d", rec.Code)
	}
	return p, b, db, &calls
}

func TestAccounts(t *testing.T) {
	ctx := context.Background()
	p, b, db, calls := signedInPanel(t)
	playing := &fakePlaying{}
	p.Playing = playing
	b.h = p.Handler()
	acct, owner, _ := db.CreateAccount(ctx, "person-1", store.NewOwner{Name: "Main"})
	_ = db.SetPIN(ctx, acct.ID, owner.ID, []byte("hash"))
	_ = db.RecordPIN(ctx, owner.ID, 5, time.Now(), time.Now().Add(time.Hour))
	sess, _, _ := db.CreateSession(ctx, acct.ID, owner.ID, "Firefox on Linux", time.Hour)

	rec := b.do("GET", "/admin/api/accounts", "", nil)
	body := rec.Body.String()
	for _, want := range []string{`"subject":"person-1"`, `"status":"active"`, `"pinLocked":true`, `"device":"Firefox on Linux"`} {
		if !strings.Contains(body, want) {
			t.Errorf("list lacks %s: %s", want, body)
		}
	}
	if strings.Contains(body, `"t"`) || strings.Contains(body, "jellyfinToken") {
		t.Errorf("a token in the list: %s", body)
	}

	family, _ := db.CreateTemplate(ctx, "Family", 3, 5, false)
	change := `{"label":"Bob","templateId":"` + family.ID + `","expiresAt":"2020-01-01T00:00:00Z"}`
	rec = b.do("PATCH", "/admin/api/accounts/"+acct.ID, change, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"label":"Bob"`) || !strings.Contains(rec.Body.String(), `"status":"expired"`) ||
		!strings.Contains(rec.Body.String(), `"name":"Family"`) {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	}
	// On another template and expired: its playbacks end. Its label alone,
	// the form sending the same template: they don't.
	if !slices.Equal(playing.ended, []string{acct.ID + "/"}) {
		t.Errorf("ended: %v", playing.ended)
	}
	playing.ended = nil
	if rec := b.do("PATCH", "/admin/api/accounts/"+acct.ID, `{"label":"Bobby","templateId":"`+family.ID+`"}`, nil); rec.Code != 200 || len(playing.ended) != 0 {
		t.Errorf("label only: %d, ended %v", rec.Code, playing.ended)
	}
	b.do("PATCH", "/admin/api/accounts/"+acct.ID, `{"label":"Bob"}`, nil)
	for _, bad := range []string{`{"label":"<b>x</b>"}`, `{"templateId":"tmpl_nope"}`, `{"expiresAt":"tomorrow"}`, `{"owner":true}`} {
		if rec := b.do("PATCH", "/admin/api/accounts/"+acct.ID, bad, nil); rec.Code != 400 {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	// A refused change changes nothing, its valid parts included.
	b.do("PATCH", "/admin/api/accounts/"+acct.ID, `{"label":"Robert","templateId":"tmpl_nope"}`, nil)
	if a, _ := db.AccountByID(ctx, acct.ID); a.Label != "Bob" {
		t.Errorf("half applied: %q", a.Label)
	}
	b.do("PATCH", "/admin/api/accounts/"+acct.ID, `{"expiresAt":"","disabled":true}`, nil)
	if a, _ := db.AccountByID(ctx, acct.ID); !a.Disabled || !a.Expires.IsZero() {
		t.Errorf("after disabling and clearing the expiry: %+v", a)
	}

	if rec := b.do("POST", "/admin/api/profiles/"+owner.ID+"/unlock-pin", "{}", nil); rec.Code != 204 {
		t.Errorf("unlock: %d", rec.Code)
	}
	if locks, _ := db.PINLocks(ctx, acct.ID); locks[owner.ID] {
		t.Error("still locked")
	}
	if rec := b.do("DELETE", "/admin/api/accounts/"+acct.ID+"/sessions/"+sess.ID, "", nil); rec.Code != 204 {
		t.Errorf("end session: %d", rec.Code)
	}

	rec = b.do("DELETE", "/admin/api/accounts/"+acct.ID, "", nil)
	if rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if _, err := db.AccountByID(ctx, acct.ID); err == nil {
		t.Error("account still there")
	}
	// Profiles have no Jellyfin user: nothing to delete there.
	if strings.Contains(strings.Join(*calls, " "), "DELETE /Users/") {
		t.Errorf("a Jellyfin user was deleted: %v", *calls)
	}
}

// Without signing in, none of it.
func TestAccountsNeedSignIn(t *testing.T) {
	p, _, _, _ := signedInPanel(t)
	stranger := &browser{h: p.Handler(), cookies: map[string]string{}}
	for _, r := range [][2]string{{"GET", "/admin/api/accounts"}, {"PATCH", "/admin/api/accounts/acct_x"}, {"DELETE", "/admin/api/accounts/acct_x"}} {
		body := ""
		if r[0] == "PATCH" {
			body = "{}"
		}
		if rec := stranger.do(r[0], r[1], body, nil); rec.Code != 401 {
			t.Errorf("%s %s: %d", r[0], r[1], rec.Code)
		}
	}
}

type fakeDirectory struct {
	people map[string]connector.Person
	err    error
}

func (f *fakeDirectory) People(context.Context) (map[string]connector.Person, error) {
	return f.people, f.err
}

func TestAccountsWithNames(t *testing.T) {
	ctx := context.Background()
	p, b, db, _ := signedInPanel(t)
	db.CreateAccount(ctx, "person-1", store.NewOwner{Name: "Main"})
	dir := &fakeDirectory{people: map[string]connector.Person{"person-1": {Username: "alice", Name: "Alice", Active: true}}}
	p.Directory = dir

	body := b.do("GET", "/admin/api/accounts", "", nil).Body.String()
	if !strings.Contains(body, `"username":"alice"`) || !strings.Contains(body, `"directory":"ok"`) {
		t.Errorf("with a connector: %s", body)
	}

	// A failing connector doesn't break the list.
	p.Directory = &fakeDirectory{err: io.ErrUnexpectedEOF}
	rec := b.do("GET", "/admin/api/accounts", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"directory":"unreachable"`) || strings.Contains(rec.Body.String(), "alice") {
		t.Errorf("failing connector: %d %s", rec.Code, rec.Body)
	}
}

func TestTemplates(t *testing.T) {
	ctx := context.Background()
	_, b, db, _ := signedInPanel(t)
	acct, _, _ := db.CreateAccount(ctx, "person-1", store.NewOwner{Name: "Main"})

	body := b.do("GET", "/admin/api/templates", "", nil).Body.String()
	if !strings.Contains(body, `"name":"Default","maxStreams":1,"maxProfiles":5,"reserveStreams":false,"accounts":1,"default":true`) {
		t.Fatalf("templates: %s", body)
	}
	rec := b.do("POST", "/admin/api/templates", `{"name":"Family","maxStreams":3,"maxProfiles":5}`, nil)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	id := between(rec.Body.String(), `"id":"`, `"`)
	for _, bad := range []string{`{"name":"","maxStreams":1,"maxProfiles":1}`, `{"name":"X","maxStreams":0,"maxProfiles":1}`,
		`{"name":"X","maxStreams":1,"maxProfiles":21}`, `{"name":"Family","maxStreams":1,"maxProfiles":1}`} {
		if rec := b.do("POST", "/admin/api/templates", bad, nil); rec.Code != 400 {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	b.do("PATCH", "/admin/api/accounts/"+acct.ID, `{"templateId":"`+id+`"}`, nil)
	// Editing the template changes the account at once.
	if rec := b.do("PUT", "/admin/api/templates/"+id, `{"name":"Family","maxStreams":4,"maxProfiles":5}`, nil); rec.Code != 204 {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if a, _ := db.AccountByID(ctx, acct.ID); a.Template.MaxStreams != 4 {
		t.Errorf("account after the edit: %+v", a.Template)
	}
	if rec := b.do("DELETE", "/admin/api/templates/"+id, "", nil); rec.Code != 400 {
		t.Errorf("deleting a template in use: %d", rec.Code)
	}
	if rec := b.do("DELETE", "/admin/api/templates/tmpl_default", "", nil); rec.Code != 400 {
		t.Errorf("deleting Default: %d", rec.Code)
	}
	// Default is edited like any other.
	if rec := b.do("PUT", "/admin/api/templates/tmpl_default", `{"name":"Everyone","maxStreams":2,"maxProfiles":3}`, nil); rec.Code != 204 {
		t.Fatalf("editing Default: %d %s", rec.Code, rec.Body)
	}
	if ts, _ := db.Templates(ctx); ts[0].ID != "tmpl_default" || ts[0].Name != "Everyone" || ts[0].MaxProfiles != 3 {
		t.Errorf("Default after the edit: %+v", ts[0])
	}
}

// A stream reserved for each profile: off unless asked for, and refused
// with more profiles than streams.
func TestTemplateReservedStreams(t *testing.T) {
	ctx := context.Background()
	_, b, db, _ := signedInPanel(t)
	rec := b.do("POST", "/admin/api/templates", `{"name":"Family","maxStreams":3,"maxProfiles":3}`, nil)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"reserveStreams":false`) {
		t.Fatalf("off by default: %d %s", rec.Code, rec.Body)
	}
	id := between(rec.Body.String(), `"id":"`, `"`)
	if rec := b.do("PUT", "/admin/api/templates/"+id, `{"name":"Family","maxStreams":3,"maxProfiles":4,"reserveStreams":true}`, nil); rec.Code != 400 {
		t.Errorf("4 profiles, 3 streams: %d", rec.Code)
	}
	if rec := b.do("POST", "/admin/api/templates", `{"name":"Big","maxStreams":2,"maxProfiles":5,"reserveStreams":true}`, nil); rec.Code != 400 {
		t.Errorf("5 profiles, 2 streams: %d", rec.Code)
	}
	if rec := b.do("PUT", "/admin/api/templates/"+id, `{"name":"Family","maxStreams":3,"maxProfiles":3,"reserveStreams":true}`, nil); rec.Code != 204 {
		t.Fatalf("as many profiles as streams: %d %s", rec.Code, rec.Body)
	}
	ts, _ := db.Templates(ctx)
	for _, tm := range ts {
		if tm.ReserveStreams != (tm.ID == id) {
			t.Errorf("%s: reserve %v", tm.Name, tm.ReserveStreams)
		}
	}
	// Turned off again, the limits are free again.
	if rec := b.do("PUT", "/admin/api/templates/"+id, `{"name":"Family","maxStreams":3,"maxProfiles":5}`, nil); rec.Code != 204 {
		t.Errorf("off: %d %s", rec.Code, rec.Body)
	}
}

func between(s, start, end string) string {
	_, after, ok := strings.Cut(s, start)
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(after, end)
	return v
}

// An account that brought a history in says when; an admin can let it
// bring one again.
func TestAllowHistoryCopy(t *testing.T) {
	_, b, db, _ := signedInPanel(t)
	ctx := context.Background()
	acct, _, _ := db.CreateAccount(ctx, "person-9", store.NewOwner{Name: "Main"})
	if err := db.RecordCopy(ctx, "old-server", "old-user", acct.ID); err != nil {
		t.Fatal(err)
	}
	if rec := b.do("GET", "/admin/api/accounts", "", nil); !strings.Contains(rec.Body.String(), `"historyCopiedAt"`) {
		t.Errorf("not shown: %s", rec.Body)
	}
	if rec := b.do("DELETE", "/admin/api/accounts/"+acct.ID+"/history-copy", "", nil); rec.Code != 204 {
		t.Fatalf("allow again: %d %s", rec.Code, rec.Body)
	}
	if _, done, _ := db.AccountCopy(ctx, acct.ID); done {
		t.Error("still recorded")
	}
	if into, _ := db.CopiedInto(ctx, "old-server", "old-user"); into != "" {
		t.Error("the old account isn't free")
	}
}

func TestLanguagesRoutes(t *testing.T) {
	p, b, _, _ := signedInPanel(t)
	changes := 0
	p.OnLanguage = func() { changes++ }
	// A fresh install: English (United States), no country yet.
	if rec := b.do("GET", "/admin/api/languages", "", nil); !strings.Contains(rec.Body.String(), `"default":"en-US"`) ||
		!strings.Contains(rec.Body.String(), `"country":""`) || !strings.Contains(rec.Body.String(), `"CH":[{"tag":"de-CH","name":"Deutsch (Schweiz)"},{"tag":"fr-CH","name":"Français (Suisse)"},{"tag":"it-CH","name":"Italiano (Svizzera)"}]`) {
		t.Errorf("languages: %s", rec.Body)
	}
	for body, want := range map[string]string{
		`{"country":"CA","offered":[],"default":"en-US"}`:                "at least one",
		`{"country":"CA","offered":["fr-CA","xx-YY"],"default":"fr-CA"}`: "each once",
		`{"country":"CA","offered":["fr-CA","fr-CA"],"default":"fr-CA"}`: "each once",
		`{"country":"CA","offered":["fr-CA","en-CA"],"default":"de-DE"}`: "one of those offered",
		`{"country":"XX","offered":["en-US"],"default":"en-US"}`:         "no languages for that country",
		// Not Canada's, and not offered before.
		`{"country":"CA","offered":["fr-CA","fr-FR"],"default":"fr-CA"}`: "each once",
	} {
		if rec := b.do("PUT", "/admin/api/languages", body, nil); rec.Code != 400 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	// English (United States) was offered: it can stay.
	rec := b.do("PUT", "/admin/api/languages", `{"country":"CA","offered":["fr-CA","en-CA","en-US"],"default":"fr-CA"}`, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"offered":["fr-CA","en-CA","en-US"]`) ||
		!strings.Contains(rec.Body.String(), `"country":"CA"`) || changes != 1 {
		t.Errorf("saved: %d %s, changes %d", rec.Code, rec.Body, changes)
	}
	// Another country: Canada's languages stay as long as they're kept.
	rec = b.do("PUT", "/admin/api/languages", `{"country":"FR","offered":["fr-CA","fr-FR"],"default":"fr-CA"}`, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"country":"FR"`) || changes != 1 {
		t.Errorf("another country: %d %s, changes %d", rec.Code, rec.Body, changes)
	}
	// Dropped, they're gone: English (Canada) can't come back from France.
	if rec := b.do("PUT", "/admin/api/languages", `{"country":"FR","offered":["fr-CA","fr-FR","en-CA"],"default":"fr-CA"}`, nil); rec.Code != 400 {
		t.Errorf("back from before: %d %s", rec.Code, rec.Body)
	}
	// No country sent: the one saved.
	if rec := b.do("PUT", "/admin/api/languages", `{"offered":["fr-FR"],"default":"fr-CA"}`, nil); rec.Code != 400 {
		t.Errorf("default dropped: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do("PUT", "/admin/api/languages", `{"offered":["fr-FR","fr-CA"],"default":"fr-CA"}`, nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"country":"FR"`) {
		t.Errorf("no country: %d %s", rec.Code, rec.Body)
	}
}

// The ratings country follows the server's country, unless the operator
// chose one in the TMDB card.
func TestLanguagesMoveTheRatingsCountry(t *testing.T) {
	_, b, db, _ := signedInPanel(t)
	ratings := func() string {
		t.Helper()
		rec := b.do("GET", "/admin/api/ratings", "", nil)
		var out struct{ Country, Chosen, Server, Region string }
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return out.Country + " " + out.Chosen + " " + out.Server + " " + out.Region
	}
	if got := ratings(); got != "   " {
		t.Errorf("fresh: %q", got)
	}
	b.do("PUT", "/admin/api/languages", `{"country":"CA","offered":["fr-CA"],"default":"fr-CA"}`, nil)
	if got := ratings(); got != "CA  CA CA" {
		t.Errorf("Canada: %q", got)
	}
	// Poland: Overfin doesn't read its ratings.
	b.do("PUT", "/admin/api/languages", `{"country":"PL","offered":["fr-CA","pl-PL"],"default":"fr-CA"}`, nil)
	if got := ratings(); got != "  PL " {
		t.Errorf("Poland: %q", got)
	}
	if err := db.SetRatingsCountry(context.Background(), "US"); err != nil {
		t.Fatal(err)
	}
	b.do("PUT", "/admin/api/languages", `{"country":"CA","offered":["fr-CA"],"default":"fr-CA"}`, nil)
	if got := ratings(); got != "US US CA CA" {
		t.Errorf("chosen: %q", got)
	}
}

// Names typed in the panel: up to their length in characters, accented
// ones counting one each (not in bytes), and spaces around or doubled
// inside tidied rather than refused.
func TestPanelNames(t *testing.T) {
	ctx := context.Background()
	_, b, db, _ := signedInPanel(t)
	acct, _, _ := db.CreateAccount(ctx, "person-1", store.NewOwner{Name: "Main"})
	// 37 characters, 43 bytes.
	const long = "Séries et émissions éducatives à télé"

	rec := b.do("POST", "/admin/api/categories", `{"names":{"fr":"`+long+`","en":"  Kids   shows "},"rules":{}}`, nil)
	if rec.Code != 201 {
		t.Fatalf("category: %d %s", rec.Code, rec.Body)
	}
	if body := b.do("GET", "/admin/api/categories", "", nil).Body.String(); !strings.Contains(body, `"en":"Kids shows"`) || !strings.Contains(body, `"fr":"`+long+`"`) {
		t.Errorf("category names: %s", body)
	}
	rec = b.do("POST", "/admin/api/templates", `{"name":" `+long+` ","maxStreams":1,"maxProfiles":1}`, nil)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"name":"`+long+`"`) {
		t.Errorf("template: %d %s", rec.Code, rec.Body)
	}
	rec = b.do("PATCH", "/admin/api/accounts/"+acct.ID, `{"label":"Bob "}`, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"label":"Bob"`) {
		t.Errorf("label: %d %s", rec.Code, rec.Body)
	}
	// Still refused: too long in characters, markup, nothing but spaces.
	for _, bad := range []struct{ method, path, body string }{
		{"POST", "/admin/api/categories", `{"names":{"fr":"` + strings.Repeat("é", 41) + `"},"rules":{}}`},
		{"POST", "/admin/api/categories", `{"names":{"fr":"<b>x</b>"},"rules":{}}`},
		{"POST", "/admin/api/categories", `{"names":{"fr":"   "},"rules":{}}`},
		{"POST", "/admin/api/templates", `{"name":"` + strings.Repeat("é", 41) + `","maxStreams":1,"maxProfiles":1}`},
		{"POST", "/admin/api/templates", `{"name":"  ","maxStreams":1,"maxProfiles":1}`},
		{"PATCH", "/admin/api/accounts/" + acct.ID, `{"label":"` + strings.Repeat("é", 65) + `"}`},
	} {
		if rec := b.do(bad.method, bad.path, bad.body, nil); rec.Code != 400 {
			t.Errorf("%s %s: %d", bad.path, bad.body, rec.Code)
		}
	}
}
