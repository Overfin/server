package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/provision"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

// sent keeps the invitations sent by email.
type sent struct {
	links []string
	// released are the people whose invitation's use was given back.
	released []string
	// setups are the setup links made, "subject send".
	setups []string
	noMail bool
}

func (l *sent) CanMail() bool { return !l.noMail }

func (l *sent) SetupLink(_ context.Context, subject string, send bool) (string, time.Time, error) {
	l.setups = append(l.setups, fmt.Sprint(subject, " ", send))
	if send {
		return "", time.Now().Add(7 * 24 * time.Hour), nil
	}
	return "https://media.example.org/setup#setup=x", time.Now().Add(7 * 24 * time.Hour), nil
}

func (l *sent) ReleaseInvitation(_ context.Context, subject string) error {
	l.released = append(l.released, subject)
	return nil
}

func (l *sent) SendInvitation(_ context.Context, to, link string) error {
	l.links = append(l.links, to+" "+link)
	return nil
}

func TestPeople(t *testing.T) {
	box, _ := seal.New(bytes.Repeat([]byte{3}, 32))
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	me, _ := db.CreateUser(context.Background(), "me@example.org", true)
	made := &sent{}
	p := &Panel{Features: &memFeatures{f: config.DefaultFeatures()}, Store: db, People: made,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		SiteAdmin: func(*http.Request) (string, bool, bool) { return "me@example.org", true, true }}
	h := p.SiteHandler(false)
	login := httptest.NewRecorder()
	h.ServeHTTP(login, httptest.NewRequest("GET", "/admin/login", nil))
	cookie := login.Result().Cookies()[0]
	do := func(method, path string, body any) (int, map[string]any) {
		var r io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, r)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	if code, out := do("POST", "/admin/api/people", map[string]any{"email": "Bob <bob@example.org>"}); code != 400 {
		t.Errorf("not a plain email: %d %v", code, out)
	}
	// Nobody types a password any more.
	if code, _ := do("POST", "/admin/api/people", map[string]any{"email": "bob@example.org", "password": "first password 1"}); code != 400 {
		t.Errorf("a typed password: %d", code)
	}
	code, bob := do("POST", "/admin/api/people", map[string]any{"email": "bob@example.org"})
	if code != 200 || bob["admin"] != false || bob["waitingSetup"] != true || bob["hasPassword"] != false || bob["mustChange"] != false {
		t.Fatalf("adding Bob: %d %v", code, bob)
	}
	// A random password nobody knows, hashed.
	if u, _ := db.UserByEmail(context.Background(), "bob@example.org"); u.PasswordHash == "" || !u.SetupPending || !u.Confirmed || u.MustChange {
		t.Errorf("Bob: %+v", u)
	}
	if code, _ := do("POST", "/admin/api/people", map[string]any{"email": "BOB@example.org"}); code != 409 {
		t.Errorf("the same email twice: %d", code)
	}
	id := bob["id"].(string)
	if code, out := do("PATCH", "/admin/api/people/"+id, map[string]any{"admin": true}); code != 200 || out["admin"] != true {
		t.Errorf("making Bob an admin: %d %v", code, out)
	}
	// The setup link: copied, or emailed; not emailed without email.
	if code, out := do("POST", "/admin/api/people/"+id+"/setup-link", map[string]any{}); code != 200 || out["link"] != "https://media.example.org/setup#setup=x" ||
		out["sent"] != false || out["expires"] == nil {
		t.Errorf("copying the link: %d %v", code, out)
	}
	if code, out := do("POST", "/admin/api/people/"+id+"/setup-link", map[string]any{"send": true}); code != 200 || out["link"] != nil || out["sent"] != true {
		t.Errorf("emailing the link: %d %v", code, out)
	}
	made.noMail = true
	if code, _ := do("POST", "/admin/api/people/"+id+"/setup-link", map[string]any{"send": true}); code != 400 {
		t.Errorf("emailing without email: %d", code)
	}
	made.noMail = false
	if len(made.setups) != 2 || made.setups[0] != id+" false" || made.setups[1] != id+" true" {
		t.Errorf("setup links: %v", made.setups)
	}
	if code, _ := do("POST", "/admin/api/people/nobody/setup-link", map[string]any{}); code != 404 {
		t.Errorf("nobody's link: %d", code)
	}
	// The list says when the link ends; cancelling it ends it.
	db.AddSetupLink(context.Background(), store.EmailLink{Subject: id, LinkHash: []byte("L"), Purpose: "setup", Expires: time.Now().Add(time.Hour)})
	_, list := do("GET", "/admin/api/people", nil)
	var listed map[string]any
	for _, it := range list["items"].([]any) {
		if it.(map[string]any)["id"] == id {
			listed = it.(map[string]any)
		}
	}
	if listed["setupExpires"] == nil || listed["waitingSetup"] != true {
		t.Errorf("Bob listed: %v", listed)
	}
	if code, _ := do("DELETE", "/admin/api/people/"+id+"/setup-link", nil); code != 204 {
		t.Errorf("cancelling: %d", code)
	}
	if at, _ := db.SetupLinkExpires(context.Background(), id); !at.IsZero() {
		t.Errorf("still a link: %v", at)
	}
	// The old way to set a password is gone.
	if code, _ := do("PUT", "/admin/api/people/"+id+"/password", map[string]any{"password": "second password 2"}); code != 404 && code != 405 {
		t.Errorf("an admin typing a password: %d", code)
	}
	// What a copied link does: reminder by default, or hold.
	if code, out := do("GET", "/admin/api/registration", nil); code != 200 || out["copiedSetup"] != "reminder" {
		t.Errorf("default: %d %v", code, out)
	}
	if code, _ := do("PUT", "/admin/api/registration", map[string]any{"rules": map[string]any{"minLength": 10, "digit": true, "special": true}, "copiedSetup": "other"}); code != 400 {
		t.Errorf("an unknown choice: %d", code)
	}
	if code, out := do("PUT", "/admin/api/registration", map[string]any{"rules": map[string]any{"minLength": 10, "digit": true, "special": true}, "copiedSetup": "hold"}); code != 200 || out["copiedSetup"] != "hold" {
		t.Errorf("hold: %d %v", code, out)
	}
	// Invitations by email, and the rules, on the registration page.
	if code, out := do("POST", "/admin/api/registration/send", map[string]any{"email": "ann@example.org", "link": "https://media.example.org/invite/abc"}); code != 200 ||
		len(made.links) != 1 || made.links[0] != "ann@example.org https://media.example.org/invite/abc" {
		t.Errorf("sending an invitation: %d %v %v", code, out, made.links)
	}
	if code, _ := do("POST", "/admin/api/registration/send", map[string]any{"email": "ann@example.org", "link": "https://evil.example/x"}); code != 400 {
		t.Errorf("not an invitation's link: %d", code)
	}
	if code, out := do("PUT", "/admin/api/registration", map[string]any{"rules": map[string]any{"minLength": 9}}); code != 400 ||
		out["detail"] != "The minimum length must be from 10 to 64 characters." {
		t.Errorf("under 10: %d %v", code, out)
	}
	if code, out := do("PUT", "/admin/api/registration", map[string]any{"rules": map[string]any{"minLength": 12, "digit": true}}); code != 200 ||
		out["rules"].(map[string]any)["minLength"] != float64(12) || out["mail"] != true {
		t.Errorf("new rules: %d %v", code, out)
	}
	// Admins keep themselves: no stepping down, no removing themselves.
	if code, _ := do("PATCH", "/admin/api/people/"+me.Subject, map[string]any{"admin": false}); code != 400 {
		t.Errorf("stepping down: %d", code)
	}
	if code, _ := do("DELETE", "/admin/api/people/"+me.Subject, nil); code != 400 {
		t.Errorf("removing myself: %d", code)
	}
	// Nor through their account, in Users: disabled or deleted, they'd lose
	// the panel.
	mine, _, _ := db.CreateAccount(context.Background(), me.Subject, store.NewOwner{Name: "Me"})
	if code, _ := do("PATCH", "/admin/api/accounts/"+mine.ID, map[string]any{"expiresAt": "2000-01-01T00:00:00Z"}); code != 400 {
		t.Errorf("ending one's own access: %d", code)
	}
	if code, _ := do("PATCH", "/admin/api/accounts/"+mine.ID, map[string]any{"disabled": true}); code != 400 {
		t.Errorf("disabling my account: %d", code)
	}
	if code, _ := do("DELETE", "/admin/api/accounts/"+mine.ID, nil); code != 400 {
		t.Errorf("deleting my account: %d", code)
	}
	if a, _ := db.AccountByID(context.Background(), mine.ID); a == nil || a.Disabled {
		t.Errorf("my account: %+v", a)
	}
	code, list = do("GET", "/admin/api/people", nil)
	if items := list["items"].([]any); code != 200 || len(items) != 2 || items[1].(map[string]any)["you"] != true {
		t.Errorf("people: %d %v", code, list)
	}
	if code, _ := do("DELETE", "/admin/api/people/"+id, nil); code != 204 {
		t.Errorf("removing Bob: %d", code)
	}
	if len(made.released) != 1 || made.released[0] != id {
		t.Errorf("Bob's invitation not given back: %v", made.released)
	}
	if _, err := db.UserByEmail(context.Background(), "bob@example.org"); err != store.ErrNotFound {
		t.Errorf("Bob is still there: %v", err)
	}
}

// TestLogsNameNoAdmin: the panel's logs name the admin by a keyed hash of
// their subject, never by their email (SECURITY.md rule 54), while the
// panel still shows their email.
func TestLogsNameNoAdmin(t *testing.T) {
	box, _ := seal.New(bytes.Repeat([]byte{3}, 32))
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	me, _ := db.CreateUser(context.Background(), "me@example.org", true)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	prov := &provision.Provisioner{Store: db, LogKey: []byte("k"), Logger: logger}
	p := &Panel{Features: &memFeatures{f: config.DefaultFeatures()}, Store: db, Logger: logger,
		Provisioner: func() *provision.Provisioner { return prov },
		SiteAdmin:   func(*http.Request) (string, bool, bool) { return "me@example.org", true, true }}
	h := p.SiteHandler(false)
	login := httptest.NewRecorder()
	h.ServeHTTP(login, httptest.NewRequest("GET", "/admin/login", nil))
	cookie := login.Result().Cookies()[0]
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do("GET", "/admin/api/session", ""); !strings.Contains(rec.Body.String(), "me@example.org") {
		t.Errorf("the panel shows: %s", rec.Body)
	}
	if rec := do("PUT", "/admin/api/languages", `{"offered":["en-US"],"default":"en-US"}`); rec.Code != 200 {
		t.Fatalf("changing the languages: %d %s", rec.Code, rec.Body)
	}
	out := logs.String()
	if strings.Contains(out, "example.org") {
		t.Errorf("an email in the logs:\n%s", out)
	}
	if !strings.Contains(out, "the languages changed") || !strings.Contains(out, "by="+prov.LogID(me.Subject)) {
		t.Errorf("the admin isn't named by their hash:\n%s", out)
	}
}
