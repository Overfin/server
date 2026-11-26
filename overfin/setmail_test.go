package overfin

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/emails"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

// smtpServer accepts emails without TLS, refuses those to nobody@, and
// keeps the recipients of those it took.
func smtpServer(t *testing.T) (port int, took func() []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var to []string
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				r := bufio.NewReader(conn)
				say := func(s string) { conn.Write([]byte(s + "\r\n")) }
				say("220 fake")
				rcpt := ""
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
					case strings.HasPrefix(cmd, "EHLO"):
						say("250 fake")
					case strings.HasPrefix(cmd, "RCPT") && strings.Contains(cmd, "NOBODY@"):
						say("550 5.1.1 Recipient address rejected")
					case strings.HasPrefix(cmd, "RCPT"):
						rcpt = strings.ToLower(strings.TrimSpace(line[strings.Index(line, ":")+1:]))
						say("250 ok")
					case cmd == "DATA":
						say("354 go on")
						for {
							l, err := r.ReadString('\n')
							if err != nil || l == ".\r\n" {
								break
							}
						}
						mu.Lock()
						to = append(to, rcpt)
						mu.Unlock()
						say("250 ok")
					case cmd == "QUIT":
						say("221 bye")
						return
					default:
						say("250 ok")
					}
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := to
		to = nil
		return out
	}
}

// The SMTP server set from the panel: tested with an email first, and
// changed only once it went through; bad settings said in plain words;
// a username without its password refused; email turned off with no
// host; nothing changes when the configuration file sets email.
func TestSetMail(t *testing.T) {
	ctx := context.Background()
	box, _ := seal.New(bytes.Repeat([]byte{4}, 32))
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "o.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	g := &gateway{sh: &shared{db: db, emails: &emails.Writer{Store: db, Logger: logger}, logger: logger}, cfg: &config.Config{}}
	port, took := smtpServer(t)
	good := config.Mail{Host: " 127.0.0.1 ", Port: port, TLS: "none", From: "Overfin <o@example.org>"}

	// The test email refused: nothing changes.
	if _, err := g.SetMail(ctx, good, "", "nobody@example.org"); err == nil || !strings.Contains(err.Error(), "nothing changed") {
		t.Errorf("a refused test: %v", err)
	}
	if m, _, _ := g.Mail(); m.Host != "" {
		t.Errorf("set after a refused test: %+v", m)
	}
	if _, _, ok, _ := db.Mail(ctx); ok {
		t.Error("saved after a refused test")
	}

	summary, err := g.SetMail(ctx, good, "", "admin@example.org")
	if err != nil || !strings.Contains(summary, "admin@example.org") {
		t.Fatalf("set: %q %v", summary, err)
	}
	if got := took(); len(got) != 1 || !strings.Contains(got[0], "admin@example.org") {
		t.Errorf("the test email went to %q", got)
	}
	if m, hasPassword, inFile := g.Mail(); m.Host != "127.0.0.1" || m.Port != port || hasPassword || inFile {
		t.Errorf("in effect: %+v %v %v", m, hasPassword, inFile)
	}
	if raw, _, ok, _ := db.Mail(ctx); !ok || !strings.Contains(string(raw), `"host":"127.0.0.1"`) {
		t.Errorf("saved: %s %v", raw, ok)
	}

	// Refused before anything is sent.
	for _, c := range []struct {
		m    config.Mail
		want string
	}{
		{config.Mail{Host: "smtp.example.org", From: "o@example.org", Username: "o"}, "password is needed"},
		{config.Mail{Host: "smtp/example", From: "o@example.org"}, "host must be"},
		{config.Mail{Host: "smtp.example.org", From: "not an address"}, "from must be"},
		{config.Mail{Host: "smtp.example.org", TLS: "none", From: "o@example.org", Username: "o"}, "needs TLS"},
	} {
		_, err := g.SetMail(ctx, c.m, "", "admin@example.org")
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "mail.") {
			t.Errorf("%+v: %v", c.m, err)
		}
	}
	if got := took(); len(got) != 0 {
		t.Errorf("sent for refused settings: %q", got)
	}
	if m, _, _ := g.Mail(); m.Host != "127.0.0.1" {
		t.Errorf("a refusal changed what's in effect: %+v", m)
	}

	// Off, with no host.
	if summary, err := g.SetMail(ctx, config.Mail{Host: "  "}, "", "admin@example.org"); err != nil || !strings.Contains(summary, "off") {
		t.Errorf("off: %q %v", summary, err)
	}
	if m, _, _ := g.Mail(); m.Host != "" {
		t.Errorf("still on: %+v", m)
	}
	if _, _, ok, _ := db.Mail(ctx); ok {
		t.Error("still saved")
	}

	// Set in the file: the panel can't.
	g.sh.mailInFile = true
	if _, err := g.SetMail(ctx, good, "", "admin@example.org"); err == nil || !strings.Contains(err.Error(), "configuration file") {
		t.Errorf("in the file: %v", err)
	}
	if got := took(); len(got) != 0 {
		t.Errorf("sent with email in the file: %q", got)
	}
}

// A sample of one of Overfin's emails: to the address asked, its link
// one that works for nobody; an email that isn't one, or email off,
// refused.
func TestSendSample(t *testing.T) {
	ctx := context.Background()
	box, _ := seal.New(bytes.Repeat([]byte{4}, 32))
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "o.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	g := &gateway{sh: &shared{db: db, emails: &emails.Writer{Store: db, Logger: logger}, logger: logger}, cfg: &config.Config{}}
	if err := g.SendSample(ctx, "reset", "admin@example.org"); err == nil || !strings.Contains(err.Error(), "off") {
		t.Errorf("email off: %v", err)
	}
	port, took := smtpServer(t)
	if _, err := g.SetMail(ctx, config.Mail{Host: "127.0.0.1", Port: port, TLS: "none", From: "o@example.org"}, "", "admin@example.org"); err != nil {
		t.Fatal(err)
	}
	took()
	for _, name := range emails.Names {
		if err := g.SendSample(ctx, name, "admin@example.org"); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got := took(); len(got) != len(emails.Names) {
		t.Errorf("samples sent: %d of %d", len(got), len(emails.Names))
	}
	if err := g.SendSample(ctx, "nope", "admin@example.org"); err == nil || !strings.Contains(err.Error(), "no such email") {
		t.Errorf("an email that isn't: %v", err)
	}
	if err := g.SendSample(ctx, "reset", "nobody@example.org"); err == nil || !strings.Contains(err.Error(), "didn't go through") {
		t.Errorf("refused by the server: %v", err)
	}
}

// Only this site's invitation links, and only pairing codes, go out by
// the site's email: anything else is refused before it's written, so the
// site's address never carries someone else's link.
func TestOnlyOwnLinksAndCodesEmailed(t *testing.T) {
	ctx := context.Background()
	box, _ := seal.New(bytes.Repeat([]byte{4}, 32))
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "o.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.Site.URL = "https://media.example.org/"
	g := &gateway{sh: &shared{db: db, emails: &emails.Writer{Store: db, Logger: logger}, logger: logger}, cfg: cfg}
	if err := g.SendInvitation(ctx, "a@example.org", "https://media.example.org/invite/x"); err == nil || !strings.Contains(err.Error(), "off") {
		t.Errorf("email off: %v", err)
	}
	if g.CanSendMail() {
		t.Error("can send with email off")
	}
	port, took := smtpServer(t)
	if _, err := g.SetMail(ctx, config.Mail{Host: "127.0.0.1", Port: port, TLS: "none", From: "o@example.org"}, "", "admin@example.org"); err != nil {
		t.Fatal(err)
	}
	took()
	if !g.CanSendMail() {
		t.Error("can't send with email on")
	}
	for _, link := range []string{"https://evil.example.net/invite/x", "https://media.example.org/signin", "https://media.example.org.evil.net/invite/x",
		"http://media.example.org/invite/x", "https://media.example.org/invite"} {
		if err := g.SendInvitation(ctx, "a@example.org", link); err == nil || !strings.Contains(err.Error(), "isn't one of this site's") {
			t.Errorf("%s: %v", link, err)
		}
	}
	if err := g.SendInvitation(ctx, "a@example.org", "https://media.example.org/invite/abc"); err != nil {
		t.Errorf("this site's: %v", err)
	}
	expires := time.Now().Add(24 * time.Hour)
	for _, code := range []string{"https://evil.example.net", "msfp-pair-1.x", "msfp-pair-0." + strings.Repeat("a", 1200), ""} {
		if err := g.SendPairingCode(ctx, "b@example.org", code, "", expires); err == nil || !strings.Contains(err.Error(), "isn't a pairing code") {
			t.Errorf("%.30s: %v", code, err)
		}
	}
	if err := g.SendPairingCode(ctx, "b@example.org", "msfp-pair-0.abc", "Friends", expires); err != nil {
		t.Errorf("a code: %v", err)
	}
	if got := took(); len(got) != 2 || !strings.Contains(got[0], "a@example.org") || !strings.Contains(got[1], "b@example.org") {
		t.Errorf("sent: %q", got)
	}
	// A site without an address of its own sends no invitation at all.
	g.cfg = &config.Config{}
	if err := g.SendInvitation(ctx, "a@example.org", "/invite/abc"); err == nil {
		t.Error("an invitation from a site without an address")
	}
}
