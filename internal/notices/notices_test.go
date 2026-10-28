package notices

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/overfin/server/internal/emails"
	"github.com/overfin/server/internal/mail"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

type sent struct{ to, subject string }

type fakeMail struct {
	mu   sync.Mutex
	sent []sent
}

func (f *fakeMail) SendMessage(_ context.Context, to string, m mail.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sent{to, m.Subject})
	return nil
}

func (f *fakeMail) all() []sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sent(nil), f.sent...)
}

type hook struct {
	mu    sync.Mutex
	posts []Payload
	sigs  []string
	raw   [][]byte
}

func (h *hook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var p Payload
	json.Unmarshal(body, &p)
	h.mu.Lock()
	h.posts, h.sigs, h.raw = append(h.posts, p), append(h.sigs, r.Header.Get("X-Overfin-Signature")), append(h.raw, body)
	h.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (h *hook) got() []Payload {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Payload(nil), h.posts...)
}

func setup(t *testing.T) (*Notifier, *store.Store, *fakeMail, *hook, string) {
	t.Helper()
	box, _ := seal.New(bytes.Repeat([]byte{5}, 32))
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h := &hook{}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := &fakeMail{}
	n := &Notifier{Store: db, Emails: &emails.Writer{Store: db, Logger: logger}, Managed: true, Site: "https://example.org/",
		Logger: logger, Mail: func() Sender { return m }}
	return n, db, m, h, srv.URL
}

// An account whose access ends soon is noticed once per end date, to the
// admins and the webhook (signed), and its person is warned; ended, it's
// noticed again.
func TestExpiry(t *testing.T) {
	ctx := context.Background()
	n, db, m, h, url := setup(t)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	admin, _ := db.CreateUser(ctx, "admin@example.org", true)
	person, _ := db.CreateUser(ctx, "alex@example.org", false)
	_ = admin
	acct, _, _ := db.CreateAccount(ctx, person.Subject, store.NewOwner{Name: "Alex", JellyfinUserID: strings.Repeat("a", 32), JellyfinUsername: "of-1", JellyfinToken: "t"})
	db.SetAccountExpiry(ctx, acct.ID, now.Add(3*24*time.Hour))
	// Another, with no end, and one ending later than the days set.
	db.CreateAccount(ctx, "nobody", store.NewOwner{Name: "B", JellyfinUserID: strings.Repeat("b", 32), JellyfinUsername: "of-2", JellyfinToken: "t"})
	far, _, _ := db.CreateAccount(ctx, "far", store.NewOwner{Name: "C", JellyfinUserID: strings.Repeat("c", 32), JellyfinUsername: "of-3", JellyfinToken: "t"})
	db.SetAccountExpiry(ctx, far.ID, now.AddDate(0, 0, 30))

	// Off: nothing.
	if err := n.Check(ctx, now); err != nil || len(h.got()) != 0 || len(m.all()) != 0 {
		t.Fatalf("off: %v %v %v", err, h.got(), m.all())
	}
	db.SetNoticeSettings(ctx, store.NoticeSettings{Expiring: true, Expired: true, Days: 7, Admins: true, Webhook: url, Person: true})
	key, _ := db.NewNoticeSecret(ctx)
	if err := n.Check(ctx, now); err != nil {
		t.Fatal(err)
	}
	posts := h.got()
	if len(posts) != 1 || posts[0].Event != "account.expiring" || posts[0].Account.Name != "alex@example.org" ||
		!strings.Contains(posts[0].Text, "November 4, 2026") || posts[0].Text != posts[0].Content {
		t.Fatalf("webhook: %+v", posts)
	}
	if !bytes.Contains(h.raw[0], []byte(`"allowed_mentions":{"parse":[]}`)) {
		t.Errorf("mentions allowed: %s", h.raw[0])
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(h.raw[0])
	if h.sigs[0] != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		t.Errorf("signature: %s", h.sigs[0])
	}
	mails := m.all()
	if len(mails) != 2 {
		t.Fatalf("emails: %+v", mails)
	}
	for _, s := range mails {
		switch s.to {
		case "admin@example.org":
			if !strings.Contains(s.subject, "alex@example.org") {
				t.Errorf("to the admin: %q", s.subject)
			}
		case "alex@example.org":
			if s.subject != "Your access to Overfin ends on November 4, 2026" {
				t.Errorf("to the person: %q", s.subject)
			}
		default:
			t.Errorf("to %s", s.to)
		}
	}

	// Once.
	n.Check(ctx, now.Add(time.Hour))
	if len(h.got()) != 1 || len(m.all()) != 2 {
		t.Errorf("again: %d posts, %d emails", len(h.got()), len(m.all()))
	}
	// Given more time, noticed again when that nears.
	db.SetAccountExpiry(ctx, acct.ID, now.AddDate(0, 0, 10))
	n.Check(ctx, now.AddDate(0, 0, 4))
	if posts := h.got(); len(posts) != 2 || posts[1].Event != "account.expiring" {
		t.Errorf("after more time: %+v", posts)
	}
	// Ended.
	n.Check(ctx, now.AddDate(0, 0, 11))
	if posts := h.got(); len(posts) != 3 || posts[2].Event != "account.expired" {
		t.Errorf("ended: %+v", posts)
	}
	// A disabled account is left alone.
	db.SetAccountExpiry(ctx, far.ID, now.AddDate(0, 0, 12))
	db.SetAccountDisabled(ctx, far.ID, true)
	n.Check(ctx, now.AddDate(0, 0, 11))
	if len(h.got()) != 3 {
		t.Errorf("disabled: %+v", h.got())
	}
}

// A new account is noticed when the settings say so.
func TestJoined(t *testing.T) {
	ctx := context.Background()
	n, db, _, h, url := setup(t)
	acct, _, _ := db.CreateAccount(ctx, "s", store.NewOwner{Name: "Sam", JellyfinUserID: strings.Repeat("a", 32), JellyfinUsername: "of-1", JellyfinToken: "t"})
	db.SetNoticeSettings(ctx, store.NoticeSettings{Joined: true, Days: 7, Webhook: url})
	n.Joined(acct)
	deadline := time.Now().Add(5 * time.Second)
	for len(h.got()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if posts := h.got(); len(posts) != 1 || posts[0].Event != "account.joined" || posts[0].Account.Name != "Sam" || h.sigs[0] != "" {
		t.Errorf("joined: %+v %q", posts, h.sigs)
	}
}

func TestCheckWebhook(t *testing.T) {
	for _, ok := range []string{"", "https://discord.com/api/webhooks/1/x", "http://192.168.1.5:8080/overfin"} {
		if err := CheckWebhook(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"ftp://x", "file:///etc/passwd", "https://", "https://u:p@example.org/", "not a url", "https://x/" + strings.Repeat("a", 2100)} {
		if CheckWebhook(bad) == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
