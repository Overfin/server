package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/overfin/server/internal/emails"
	"github.com/overfin/server/internal/identity"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
)

// Setup links: an account an admin makes has a random password nobody
// knows, and a link to choose its own (/setup#setup=…). The admin copies
// the link, or has Overfin email it; opening an emailed link proves the
// address. A copied one doesn't: with emails on, the server-wide choice
// (store.CopiedSetup) says whether the person signs in at once, asked to
// confirm their address later ("reminder"), or confirms it first ("hold").
// Without emails there's nothing to confirm.

// setupFor is how long a setup link works.
const setupFor = 7 * 24 * time.Hour

// ErrNoMail: the server can't send emails, so a setup link can only be
// copied.
var ErrNoMail = errors.New("this server can't send emails")

// setupPage is where a setup link leads; the page reads the link in its
// fragment.
func (m *Managed) setupPage(w http.ResponseWriter, r *http.Request) {
	renderPage(w, r, m.Store, m.Logger, page{Mode: "setup", Rules: m.rules(r.Context())})
}

// SetupLink makes a new setup link for someone (their earlier one stops
// working): emailed by Overfin with send, or returned for the admin to
// copy. The link is the site's address, or only its path when the site's
// address isn't known.
func (m *Managed) SetupLink(ctx context.Context, subject string, send bool) (link string, expires time.Time, err error) {
	u, err := m.Store.UserBySubject(ctx, subject)
	if err != nil {
		return "", time.Time{}, err
	}
	if send && !m.canMail() {
		return "", time.Time{}, ErrNoMail
	}
	token := randomToken()
	purpose := "setup"
	if send {
		purpose = "setup-sent"
	}
	expires = time.Now().Add(setupFor).Truncate(time.Second)
	if err := m.Store.AddSetupLink(ctx, store.EmailLink{Subject: u.Subject, LinkHash: m.hash(purpose, token),
		Purpose: purpose, Expires: expires}); err != nil {
		return "", time.Time{}, err
	}
	link = strings.TrimSuffix(m.SiteURL, "/") + setupPagePath + "#setup=" + token
	if !send {
		return link, expires, nil
	}
	// Sent now, not in the background: the admin is told when it fails.
	_, lang, _, err := m.Store.Languages(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	msg, err := m.Emails.Write(ctx, "setup", m.mailLang(ctx, u.Email, lang), emails.Vars{Link: link})
	if err == nil {
		err = m.mailer().SendMessage(ctx, u.Email, msg)
	}
	if err != nil {
		_ = m.Store.CancelSetupLink(ctx, u.Subject)
		return "", time.Time{}, err
	}
	return "", expires, nil
}

// setupSubject finds whose a setup link is, and which way it went; with
// take, it's used up.
func (m *Managed) setupSubject(ctx context.Context, link string, take bool) (subject, purpose string, err error) {
	if len(link) != 43 {
		return "", "", store.ErrNotFound
	}
	for _, purpose := range store.SetupPurposes {
		h := m.hash(purpose, link)
		if take {
			subject, err = m.Store.TakeEmailLink(ctx, purpose, h)
		} else {
			subject, err = m.Store.PeekEmailLink(ctx, purpose, h)
		}
		if !errors.Is(err, store.ErrNotFound) {
			return subject, purpose, err
		}
	}
	return "", "", store.ErrNotFound
}

// setupLook says which address a setup link is for, without using it up,
// so the person can tell it's theirs.
func (m *Managed) setupLook(w http.ResponseWriter, r *http.Request) {
	req, ok := m.read(w, r)
	if !ok {
		return
	}
	if !m.byIP.allow("setup "+clientFrom(identity.ClientIP(r.Context())), time.Now()) {
		m.refuse(w, r, problem.RateLimited, "tooManyTries")
		return
	}
	subject, _, err := m.setupSubject(r.Context(), req.Link, false)
	var u *store.User
	if err == nil {
		u, err = m.Store.UserBySubject(r.Context(), subject)
	}
	if errors.Is(err, store.ErrNotFound) || (err == nil && u.Disabled) {
		m.refuse(w, r, problem.SigninFailed, "oldLink")
		return
	}
	if err != nil {
		m.fail(w, r, err)
		return
	}
	writeOK(w, map[string]string{"email": u.Email})
}

// setupPassword takes a setup link and the person's first password: it
// ends every other login and signs them in, or, held until their address
// is confirmed, emails them the link that does.
func (m *Managed) setupPassword(w http.ResponseWriter, r *http.Request) {
	req, ok := m.read(w, r)
	if !ok || !m.followsRules(w, r, req.Password) {
		return
	}
	if !m.byIP.allow("setup "+clientFrom(identity.ClientIP(r.Context())), time.Now()) {
		m.refuse(w, r, problem.RateLimited, "tooManyTries")
		return
	}
	ctx := r.Context()
	// The link is looked at first, and used up only once the password is
	// hashed: a request cut short while hashing leaves it working.
	subject, purpose, err := m.setupSubject(ctx, req.Link, false)
	var u *store.User
	if err == nil {
		u, err = m.Store.UserBySubject(ctx, subject)
	}
	if errors.Is(err, store.ErrNotFound) || (err == nil && u.Disabled) {
		m.refuse(w, r, problem.SigninFailed, "oldLink")
		return
	}
	if err != nil {
		m.fail(w, r, err)
		return
	}
	hash, ok := m.hashPassword(w, r, req.Password)
	if !ok {
		return
	}
	if taken, _, err := m.setupSubject(ctx, req.Link, true); errors.Is(err, store.ErrNotFound) || (err == nil && taken != subject) {
		m.refuse(w, r, problem.SigninFailed, "oldLink") // used meanwhile
		return
	} else if err != nil {
		m.fail(w, r, err)
		return
	}
	// An emailed link proved the address; a copied one didn't, which
	// matters only when emails can confirm it, and only for someone new: an
	// account past its setup, its address confirmed (by its own link, or
	// vouched for by an admin before setup links), keeps it confirmed when
	// a setup link brings it a new password.
	hold, unverified := false, false
	proven := !u.SetupPending && u.Confirmed && !u.Unverified
	if purpose == "setup" && m.canMail() && !proven {
		choice, err := m.Store.CopiedSetup(ctx)
		if err != nil {
			m.fail(w, r, err)
			return
		}
		hold, unverified = choice == "hold", choice != "hold"
	}
	err = m.Store.SetPassword(ctx, u.Subject, hash)
	if err == nil {
		err = m.Store.SetupDone(ctx, u.Subject, hold, unverified)
	}
	if err == nil {
		err = m.Store.DeleteLogins(ctx, u.Subject)
	}
	if err != nil {
		m.fail(w, r, err)
		return
	}
	if hold {
		m.sendLink(u, "verify", verifyFor, pageLang(r, m.Store))
		writeOK(w, map[string]any{"held": true, "email": u.Email})
		return
	}
	u.MustChange, u.Confirmed = false, true
	m.startLogin(w, r, u, "/", true)
}

// addressInfo says whether the signed-in person's address still needs
// confirming (a copied setup link, with the panel's "reminder"), for the
// site's reminder; and with POST, sends the link that confirms it.
func (m *Managed) addressInfo(w http.ResponseWriter, r *http.Request) {
	_, u := m.current(r)
	if u == nil {
		problem.Send(w, problem.NotAuthenticated, "")
		return
	}
	if !m.ownerHere(w, r) {
		return
	}
	writeOK(w, map[string]any{"email": u.Email, "unconfirmed": u.Unverified && m.canMail()})
}

func (m *Managed) confirmAddress(w http.ResponseWriter, r *http.Request) {
	_, u := m.current(r)
	if u == nil {
		problem.Send(w, problem.NotAuthenticated, "")
		return
	}
	if !m.ownerHere(w, r) {
		return
	}
	if !u.Unverified || !m.canMail() {
		writeOK(w, map[string]bool{"sent": false})
		return
	}
	if !m.byEmail.allow("confirm "+strings.ToLower(u.Email), time.Now()) {
		m.refuse(w, r, problem.RateLimited, "tooManyTries")
		return
	}
	m.sendLink(u, "verify", verifyFor, m.textLang(r))
	writeOK(w, map[string]bool{"sent": true})
}
