package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/overfin/server/internal/password"
	"github.com/overfin/server/internal/store"
)

// People are those Overfin signs in itself (managed sign-in), by email
// and password: who may sign in, and who's an admin. Their accounts are in
// Users.
type People interface {
	// CanMail says whether emails with links can be sent (invitations by
	// email, forgotten passwords).
	CanMail() bool
	// SendInvitation emails an invitation's link.
	SendInvitation(ctx context.Context, to, link string) error
	// ReleaseInvitation gives back the use of an invitation held for
	// someone who registered, as they're removed.
	ReleaseInvitation(ctx context.Context, subject string) error
	// SetupLink makes someone a new setup link, to choose their password
	// (their earlier one stops working): emailed with send, else returned
	// for the admin to copy (only its path when the site's address isn't
	// known). It fails with an error saying so when send and no email can
	// be sent.
	SetupLink(ctx context.Context, subject string, send bool) (link string, expires time.Time, err error)
}

type personJSON struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	Admin       bool   `json:"admin"`
	Disabled    bool   `json:"disabled"`
	HasPassword bool   `json:"hasPassword"`
	// Confirmed is false until someone who registered confirms their
	// address; MustChange, that they choose a new password at their next
	// sign-in.
	Confirmed  bool `json:"confirmed"`
	MustChange bool `json:"mustChange"`
	// WaitingSetup: nobody chose their password yet (an admin made the
	// account); SetupExpires, when their setup link stops working, if
	// they have one. Unverified: they sign in, but their address isn't
	// proven yet (a copied setup link).
	WaitingSetup bool   `json:"waitingSetup"`
	SetupExpires string `json:"setupExpires,omitempty"`
	Unverified   bool   `json:"unverified"`
	// Registered: they registered themselves with an invitation.
	Registered bool `json:"registered"`
	// AccountID is their account, once they signed in and made one.
	AccountID string `json:"accountId,omitempty"`
	// You: the admin asking.
	You       bool   `json:"you,omitempty"`
	CreatedAt string `json:"createdAt"`
}

func (p *Panel) people(w http.ResponseWriter, r *http.Request, s *session) {
	all, err := p.Store.Users(r.Context())
	if err != nil {
		p.fail(w, "listing people", err)
		return
	}
	out := []personJSON{}
	for _, u := range all {
		pj := personOf(&u)
		pj.You = strings.EqualFold(u.Email, s.name)
		if at, err := p.Store.SetupLinkExpires(r.Context(), u.Subject); err != nil {
			p.fail(w, "listing people", err)
			return
		} else if !at.IsZero() {
			pj.SetupExpires = at.Format(time.RFC3339)
		}
		if a, err := p.Store.AccountBySubject(r.Context(), u.Subject); err == nil {
			pj.AccountID = a.ID
		} else if !errors.Is(err, store.ErrNotFound) {
			p.fail(w, "listing people", err)
			return
		}
		out = append(out, pj)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func personOf(u *store.User) personJSON {
	return personJSON{ID: u.Subject, Email: u.Email, Admin: u.Admin, Disabled: u.Disabled,
		HasPassword: u.PasswordHash != "" && !u.SetupPending, Confirmed: u.Confirmed, MustChange: u.MustChange,
		WaitingSetup: u.SetupPending, Unverified: u.Unverified, Registered: u.Registered,
		CreatedAt: u.Created.Format("2006-01-02T15:04:05Z07:00")}
}

// addPerson makes someone's account here, by their email. Nobody types a
// password: it gets a random one nobody knows, and waits for setup until
// the person chooses theirs from a setup link (setupLink).
func (p *Panel) addPerson(w http.ResponseWriter, r *http.Request, _ *session) {
	var req struct {
		Email string `json:"email"`
		Admin bool   `json:"admin"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		invalid(w, "not a valid person")
		return
	}
	email := strings.TrimSpace(req.Email)
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email || len(email) > 254 {
		invalid(w, "That isn't an email address.")
		return
	}
	// 256 random bits, hashed at once: never shown, logged or sent.
	hash, err := password.Hash(password.Random())
	if err != nil {
		p.fail(w, "adding someone", err)
		return
	}
	ctx := r.Context()
	u, err := p.Store.CreateUser(ctx, email, req.Admin)
	if errors.Is(err, store.ErrEmailTaken) {
		writeJSON(w, http.StatusConflict, map[string]string{"type": "conflict", "detail": "Someone already has that email."})
		return
	}
	if err == nil {
		err = p.Store.SetPassword(ctx, u.Subject, hash)
	}
	if err == nil {
		err = p.Store.SetSetupPending(ctx, u.Subject, true)
	}
	if err != nil {
		p.fail(w, "adding someone", err)
		return
	}
	p.Logger.Info("control panel: someone added", "admin", req.Admin)
	u.PasswordHash, u.SetupPending, u.Confirmed = hash, true, true
	writeJSON(w, http.StatusOK, personOf(u))
}

// setupLink (POST /admin/api/people/{id}/setup-link) makes someone a new
// setup link, valid 7 days, their earlier one void: emailed by Overfin
// with "send" (opening it proves their address), else answered for the
// admin to copy. For someone who forgot their password too: what they
// have keeps working until they use the link.
func (p *Panel) setupLink(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		Send bool `json:"send"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		invalid(w, "not a valid request")
		return
	}
	u, ok := p.person(w, r)
	if !ok {
		return
	}
	if u.Disabled {
		invalid(w, "Their account is disabled: enable it first.")
		return
	}
	if req.Send && !p.People.CanMail() {
		invalid(w, "This server can't send emails: copy the link instead.")
		return
	}
	link, expires, err := p.People.SetupLink(r.Context(), u.Subject, req.Send)
	if err != nil {
		p.Logger.Error("control panel: a setup link failed", "send", req.Send, "err", err)
		if req.Send {
			invalid(w, "The email didn't go through: check the email settings, or copy the link instead.")
			return
		}
		p.fail(w, "making a setup link", err)
		return
	}
	p.Logger.Info("control panel: a setup link made", "sent", req.Send, "by", s.logID)
	out := map[string]any{"sent": req.Send, "expires": expires.Format(time.RFC3339)}
	if !req.Send {
		out["link"] = link
	}
	writeJSON(w, http.StatusOK, out)
}

// cancelSetupLink (DELETE /admin/api/people/{id}/setup-link) stops
// someone's setup link working.
func (p *Panel) cancelSetupLink(w http.ResponseWriter, r *http.Request, _ *session) {
	u, ok := p.person(w, r)
	if !ok {
		return
	}
	if err := p.Store.CancelSetupLink(r.Context(), u.Subject); err != nil {
		p.fail(w, "cancelling a setup link", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// changePerson makes someone an admin or not. Admins can't take it from
// themselves: someone must stay able to come in.
func (p *Panel) changePerson(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		Admin *bool `json:"admin"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Admin == nil {
		invalid(w, "not a valid change")
		return
	}
	u, ok := p.person(w, r)
	if !ok {
		return
	}
	if strings.EqualFold(u.Email, s.name) && !*req.Admin {
		invalid(w, "You can't stop being an admin yourself; another admin can.")
		return
	}
	if err := p.Store.SetUserAdmin(r.Context(), u.Subject, *req.Admin); err != nil {
		p.fail(w, "changing someone", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"admin": *req.Admin})
}

// removePerson removes someone who has no account yet; with one, deleting
// the account removes them.
func (p *Panel) removePerson(w http.ResponseWriter, r *http.Request, s *session) {
	u, ok := p.person(w, r)
	if !ok {
		return
	}
	if strings.EqualFold(u.Email, s.name) {
		invalid(w, "You can't remove yourself.")
		return
	}
	if _, err := p.Store.AccountBySubject(r.Context(), u.Subject); err == nil {
		invalid(w, "They have an account: delete it in People, which removes them too.")
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		p.fail(w, "removing someone", err)
		return
	}
	// Someone who registered gives back the use of their invitation.
	if err := p.People.ReleaseInvitation(r.Context(), u.Subject); err != nil {
		p.fail(w, "removing someone", err)
		return
	}
	if err := p.Store.DeleteUser(r.Context(), u.Subject); err != nil {
		p.fail(w, "removing someone", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *Panel) person(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	u, err := p.Store.UserBySubject(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, "No such person")
		return nil, false
	}
	if err != nil {
		p.fail(w, "reading someone", err)
		return nil, false
	}
	return u, true
}
