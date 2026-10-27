package admin

import (
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/overfin/server/internal/notices"
	"github.com/overfin/server/internal/store"
)

// Notices: which account events the operator is told about, and where.

type noticesJSON struct {
	store.NoticeSettings
	// Managed: the admins' and people's addresses are known (Overfin's own
	// sign-in); Mail: email is set up; Signed: webhook posts are signed.
	Managed bool `json:"managed"`
	Mail    bool `json:"mail"`
	Signed  bool `json:"signed"`
}

func (p *Panel) noticeSettings(w http.ResponseWriter, r *http.Request, _ *session) {
	set, err := p.Store.NoticeSettings(r.Context())
	if err != nil {
		p.fail(w, "reading the notices' settings", err)
		return
	}
	_, signed, err := p.Store.NoticeSecret(r.Context())
	if err != nil {
		p.fail(w, "reading the webhook's key", err)
		return
	}
	writeJSON(w, http.StatusOK, noticesJSON{NoticeSettings: set, Managed: p.Notices.Managed,
		Mail: p.Notices.Mail != nil && p.Notices.Mail() != nil, Signed: signed})
}

// readNotices reads and checks settings from a request; it answers and
// returns false when they're wrong.
func readNotices(w http.ResponseWriter, r *http.Request) (store.NoticeSettings, bool) {
	var set store.NoticeSettings
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&set); err != nil {
		invalid(w, "not valid settings")
		return set, false
	}
	if set.Days < 1 || set.Days > 60 {
		invalid(w, "warn between 1 and 60 days before")
		return set, false
	}
	if err := notices.CheckWebhook(set.Webhook); err != nil {
		invalid(w, err.Error())
		return set, false
	}
	return set, true
}

func (p *Panel) setNoticeSettings(w http.ResponseWriter, r *http.Request, s *session) {
	set, ok := readNotices(w, r)
	if !ok {
		return
	}
	if err := p.Store.SetNoticeSettings(r.Context(), set); err != nil {
		p.fail(w, "saving the notices' settings", err)
		return
	}
	p.Logger.Info("notices changed in the control panel", "on", set.On(), "webhook", set.Webhook != "", "by", s.logID)
	w.WriteHeader(http.StatusNoContent)
}

// testNotices sends a test notice where the settings given (saved or not)
// say.
func (p *Panel) testNotices(w http.ResponseWriter, r *http.Request, _ *session) {
	set, ok := readNotices(w, r)
	if !ok {
		return
	}
	summary, err := p.Notices.Test(r.Context(), set)
	if err != nil {
		invalid(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"summary": summary})
}

// newNoticeSecret makes a new key for signing webhook posts, shown this
// once.
func (p *Panel) newNoticeSecret(w http.ResponseWriter, r *http.Request, s *session) {
	key, err := p.Store.NewNoticeSecret(r.Context())
	if err != nil {
		p.fail(w, "making the webhook's key", err)
		return
	}
	p.Logger.Info("a new webhook key was made in the control panel", "by", s.logID)
	writeJSON(w, http.StatusOK, map[string]string{"secret": hex.EncodeToString(key)})
}
