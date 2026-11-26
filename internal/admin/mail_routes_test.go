package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/overfin/server/internal/config"
)

// fakeMail is the SMTP settings the panel changes.
type fakeMail struct {
	m          config.Mail
	password   string
	testedTo   string
	sampleTo   string
	sampleName string
	refuse     string
}

func (f *fakeMail) Mail() (config.Mail, bool, bool) { return f.m, f.password != "", false }

func (f *fakeMail) SetMail(_ context.Context, m config.Mail, password, testTo string) (string, error) {
	if f.refuse != "" {
		return "", errors.New(f.refuse)
	}
	f.m, f.testedTo = m, testTo
	if password != "" {
		f.password = password
	}
	return "A test email was sent to " + testTo + ".", nil
}

func (f *fakeMail) MailTemplates() ([]MailTemplate, string) {
	return []MailTemplate{{Name: "verify"}, {Name: "reset", HTML: "reset.html"}}, "/config/emails"
}

func (f *fakeMail) SendSample(_ context.Context, name, to string) error {
	if name == "nope" {
		return errors.New("there's no email called nope")
	}
	f.sampleName, f.sampleTo = name, to
	return nil
}

// The SMTP server from the panel: its settings and whether it has a
// password, never the password; new settings tested with an email to the
// admin making the change; settings it refuses said in its words; an
// unknown field refused before; Overfin's emails listed, one sent as a
// sample to the admin asking.
func TestMailRoutes(t *testing.T) {
	p, b, _, _ := signedInPanel(t)
	f := &fakeMail{}
	p.Mail = f
	b.h = p.Handler()

	rec := b.do("PUT", "/admin/api/mail", `{"host":"smtp.example.org","port":587,"tls":"starttls","from":"Overfin <o@example.org>","username":"o","password":"hunter2"}`, nil)
	if rec.Code != 200 || f.password != "hunter2" || f.testedTo == "" || strings.Contains(rec.Body.String(), "hunter2") {
		t.Fatalf("set: %d %s (tested to %q)", rec.Code, rec.Body, f.testedTo)
	}
	var out struct {
		Summary string
		Mail    struct {
			Host        string
			HasPassword bool
		}
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Mail.Host != "smtp.example.org" || !out.Mail.HasPassword || !strings.Contains(out.Summary, f.testedTo) {
		t.Errorf("set answer: %+v", out)
	}
	if rec := b.do("GET", "/admin/api/mail", "", nil); rec.Code != 200 || strings.Contains(rec.Body.String(), "hunter2") ||
		!strings.Contains(rec.Body.String(), `"hasPassword":true`) {
		t.Errorf("read: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do("PUT", "/admin/api/mail", `{"host":"x","secret":"y"}`, nil); rec.Code != 400 {
		t.Errorf("an unknown field: %d", rec.Code)
	}
	f.refuse = "mail.port must be between 1 and 65535"
	if rec := b.do("PUT", "/admin/api/mail", `{"host":"x","port":99999}`, nil); rec.Code != 400 || !strings.Contains(rec.Body.String(), "65535") {
		t.Errorf("refused: %d %s", rec.Code, rec.Body)
	}

	rec = b.do("GET", "/admin/api/mail/templates", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"dir":"/config/emails"`) || !strings.Contains(rec.Body.String(), `"html":"reset.html"`) {
		t.Errorf("templates: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do("POST", "/admin/api/mail/templates/reset/test", `{}`, nil); rec.Code != 200 || f.sampleName != "reset" || f.sampleTo != f.testedTo {
		t.Errorf("sample: %d %s (%q to %q)", rec.Code, rec.Body, f.sampleName, f.sampleTo)
	}
	if rec := b.do("POST", "/admin/api/mail/templates/nope/test", `{}`, nil); rec.Code != 400 {
		t.Errorf("a sample of nothing: %d", rec.Code)
	}
}
