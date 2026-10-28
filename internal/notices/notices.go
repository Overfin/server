// Package notices tells the operator about account events: an account
// made, one whose access ends soon, one whose access ended. By email to the
// admins, by a webhook, and, before their access ends, by email to the
// person. Never what anyone watches (SECURITY.md rule 46). All off until
// the operator switches them on (control panel, Notices).
package notices

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/overfin/server/internal/emails"
	"github.com/overfin/server/internal/mail"
	"github.com/overfin/server/internal/store"
)

// Sender sends one email (mail.Sender).
type Sender interface {
	SendMessage(ctx context.Context, to string, m mail.Message) error
}

// Notifier sends the notices.
type Notifier struct {
	Store  *store.Store
	Emails *emails.Writer
	// Mail is the SMTP server in effect, nil without one.
	Mail func() Sender
	// Managed is true with Overfin's own sign-in: only then does Overfin
	// know the admins' and the people's addresses.
	Managed bool
	// Site is the site's address, for the person's email.
	Site   string
	HTTP   *http.Client
	Logger *slog.Logger

	mu sync.Mutex // one check at a time
}

// Event is something a notice tells.
type Event struct {
	Kind    string // "joined", "expiring", "expired"
	Account *store.Account
}

// WebhookClient is the webhook's HTTP client: ten seconds at most, and no
// redirects followed (rule 6, for any address Overfin posts to).
func WebhookClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// CheckWebhook says what's wrong with a webhook's address, nil when it
// may be used: http or https, with a host, 2,048 characters at most.
func CheckWebhook(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return errors.New("the webhook must be an http:// or https:// address, without a user name or password")
	}
	return nil
}

// Joined tells, in the background, that an account was made.
func (n *Notifier) Joined(acct *store.Account) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		set, err := n.Store.NoticeSettings(ctx)
		if err != nil || !set.Joined {
			return
		}
		n.send(ctx, set, Event{Kind: "joined", Account: acct})
	}()
}

// Run checks for accounts whose access ends now, then every hour, until
// ctx ends.
func (n *Notifier) Run(ctx context.Context) {
	for {
		if err := n.Check(ctx, time.Now()); err != nil && ctx.Err() == nil {
			n.Logger.Error("notices: checking accounts' access failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}

// expiredFor is how long after its end an account's access is still
// noticed as ended: when notices are switched on, older ends aren't.
const expiredFor = 3 * 24 * time.Hour

// Check sends the notices due at now about accounts' access: ending
// within the days set (to the admins and the webhook, and to the person),
// and ended.
func (n *Notifier) Check(ctx context.Context, now time.Time) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	set, err := n.Store.NoticeSettings(ctx)
	if err != nil || !set.On() {
		return err
	}
	ahead := now.AddDate(0, 0, max(set.Days, 1))
	for _, due := range []struct {
		kind     string
		on       bool
		from, to time.Time
	}{
		{"expiring", set.Expiring && (set.Admins || set.Webhook != ""), now, ahead},
		{"warned", set.Person && n.Managed, now, ahead},
		{"expired", set.Expired && (set.Admins || set.Webhook != ""), now.Add(-expiredFor), now},
	} {
		if !due.on {
			continue
		}
		accounts, err := n.Store.NoticesDue(ctx, due.kind, due.from, due.to)
		if err != nil {
			return err
		}
		for _, a := range accounts {
			if due.kind == "warned" {
				n.warn(ctx, a)
			} else {
				n.send(ctx, set, Event{Kind: due.kind, Account: a})
			}
			// Recorded even when it failed, so a broken webhook doesn't
			// send it again every hour; the log says it failed.
			if err := n.Store.NoticeSent(ctx, a.ID, due.kind, a.Expires, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// send tells the admins (by email) and the webhook about an event.
func (n *Notifier) send(ctx context.Context, set store.NoticeSettings, ev Event) {
	_, lang, _, err := n.Store.Languages(ctx)
	if err != nil {
		n.Logger.Error("notices: reading the default language failed", "err", err)
		return
	}
	v := emails.Vars{Name: n.name(ctx, ev.Account), Date: ev.Account.Expires}
	msg, err := n.Emails.Write(ctx, ev.Kind, lang, v)
	if err != nil {
		n.Logger.Error("notices: writing a notice failed", "notice", ev.Kind, "err", err)
		return
	}
	if set.Admins {
		n.toAdmins(ctx, ev.Kind, msg)
	}
	if set.Webhook != "" {
		if err := n.post(ctx, set.Webhook, ev, v, msg.Subject); err != nil {
			n.Logger.Warn("notices: the webhook failed", "notice", ev.Kind, "err", err)
		}
	}
}

// toAdmins emails the admins, each in the server's default language.
func (n *Notifier) toAdmins(ctx context.Context, kind string, msg mail.Message) {
	sender := n.sender()
	if sender == nil || !n.Managed {
		return
	}
	users, err := n.Store.Users(ctx)
	if err != nil {
		n.Logger.Error("notices: listing the admins failed", "err", err)
		return
	}
	for _, u := range users {
		if u.Admin && !u.Disabled && u.Email != "" {
			if err := sender.SendMessage(ctx, u.Email, msg); err != nil {
				n.Logger.Warn("notices: emailing an admin failed", "notice", kind, "err", err)
			}
		}
	}
}

// warn emails a person whose access ends soon, in their language.
func (n *Notifier) warn(ctx context.Context, a *store.Account) {
	sender := n.sender()
	if sender == nil {
		return
	}
	u, err := n.Store.UserBySubject(ctx, a.Subject)
	if err != nil || u.Email == "" || u.Disabled {
		return // nobody to write to
	}
	lang, err := n.Store.LanguageOf(ctx, a)
	if err != nil || lang == "" {
		_, lang, _, _ = n.Store.Languages(ctx)
	}
	msg, err := n.Emails.Write(ctx, "ending", lang, emails.Vars{Link: n.Site, Date: a.Expires})
	if err == nil {
		err = sender.SendMessage(ctx, u.Email, msg)
	}
	if err != nil {
		n.Logger.Warn("notices: emailing someone whose access ends failed", "err", err)
	}
}

func (n *Notifier) sender() Sender {
	if n.Mail == nil {
		return nil
	}
	return n.Mail()
}

// name is what notices call an account: the operator's label, else the
// person's email (with Overfin's own sign-in), else its owner profile's
// name.
func (n *Notifier) name(ctx context.Context, a *store.Account) string {
	if a.Label != "" {
		return a.Label
	}
	if n.Managed {
		if u, err := n.Store.UserBySubject(ctx, a.Subject); err == nil && u.Email != "" {
			return u.Email
		}
	}
	if profiles, err := n.Store.Profiles(ctx, a.ID); err == nil {
		for _, p := range profiles {
			if p.Owner {
				return p.Name
			}
		}
	}
	return "?"
}

// Payload is what the webhook receives. Content and Text carry the
// notice in words, as Discord and Slack read them. AllowedMentions keeps
// Discord from pinging anyone a name in the words would mention: names
// are chosen by the people they belong to ("@everyone", say).
type Payload struct {
	Event           string          `json:"event"` // account.joined, account.expiring, account.expired, test
	Content         string          `json:"content"`
	Text            string          `json:"text"`
	AllowedMentions allowedMentions `json:"allowed_mentions"`
	Account         *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"account,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	At        string `json:"at"`
}

// allowedMentions is Discord's: an empty list, no mentions at all.
type allowedMentions struct {
	Parse []string `json:"parse"`
}

// post sends an event to the webhook, signed when there's a key:
// X-Overfin-Signature is "sha256=" and the HMAC-SHA256 of the body, in hex.
func (n *Notifier) post(ctx context.Context, address string, ev Event, v emails.Vars, text string) error {
	p := Payload{Event: "test", Content: text, Text: text, AllowedMentions: allowedMentions{Parse: []string{}},
		At: time.Now().UTC().Format(time.RFC3339)}
	if ev.Account != nil {
		p.Event = "account." + ev.Kind
		p.Account = &struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}{ev.Account.ID, v.Name}
		if !ev.Account.Expires.IsZero() {
			p.ExpiresAt = ev.Account.Expires.UTC().Format(time.RFC3339)
		}
	}
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Overfin")
	req.Header.Set("X-Overfin-Event", p.Event)
	if key, ok, err := n.Store.NoticeSecret(ctx); err != nil {
		return err
	} else if ok {
		mac := hmac.New(sha256.New, key)
		mac.Write(body)
		req.Header.Set("X-Overfin-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	client := n.HTTP
	if client == nil {
		client = WebhookClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		// The address may hold a token (Discord's do): only the error's
		// kind is said, not the URL.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			return uerr.Err
		}
		return err
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("it answered %d", resp.StatusCode)
	}
	return nil
}

// Test sends a test notice where the settings say, now, and says how
// each went.
func (n *Notifier) Test(ctx context.Context, set store.NoticeSettings) (string, error) {
	var done, failed []string
	if set.Admins {
		switch {
		case !n.Managed:
			failed = append(failed, "the admins' email: only with Overfin's own sign-in")
		case n.sender() == nil:
			failed = append(failed, "the admins' email: email isn't set up (Server, Email)")
		default:
			_, lang, _, _ := n.Store.Languages(ctx)
			msg, err := n.Emails.Write(ctx, "test", lang, emails.Vars{})
			if err == nil {
				n.toAdmins(ctx, "test", msg)
				done = append(done, "the admins' email")
			} else {
				failed = append(failed, "the admins' email: "+err.Error())
			}
		}
	}
	if set.Webhook != "" {
		if err := n.post(ctx, set.Webhook, Event{Kind: "test"}, emails.Vars{}, "A test from the control panel: notices will arrive like this."); err != nil {
			failed = append(failed, "the webhook: "+err.Error())
		} else {
			done = append(done, "the webhook")
		}
	}
	if len(done) == 0 && len(failed) == 0 {
		return "", errors.New("choose where notices go first")
	}
	if len(failed) > 0 {
		msg := "Failed: " + strings.Join(failed, "; ") + "."
		if len(done) > 0 {
			msg = "Sent to " + strings.Join(done, " and ") + ". " + msg
		}
		return "", errors.New(msg)
	}
	return "Sent to " + strings.Join(done, " and ") + ".", nil
}
