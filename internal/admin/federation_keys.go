package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/overfin/server/msfp"
)

// Federation keys (MSFP core section 12, SECURITY.md rules 137 and 138):
// this server's next key, exported to keep offline; changing this
// server's key, or revoking it when it was stolen; and confirming a peer's
// new key.

// exportNextKey shows the next key's seed, once asked, for the operator to
// keep offline, with its fingerprint to check it by.
func (p *Panel) exportNextKey(w http.ResponseWriter, r *http.Request, s *session) {
	fed := p.fed(w)
	if fed == nil {
		return
	}
	text, fingerprint, err := fed.ExportNextKey(r.Context())
	switch {
	case errors.Is(err, msfp.ErrNoNextKey):
		writeJSON(w, http.StatusConflict, map[string]string{"type": "no-next-key",
			"detail": "The next key isn't on this server any more: it was deleted once exported. Use the copy you kept."})
		return
	case err != nil:
		p.fail(w, "exporting the next key", err)
		return
	}
	p.Logger.Info("the federation's next key was exported in the control panel", "by", s.logID)
	writeJSON(w, http.StatusOK, map[string]string{"text": text, "fingerprint": fingerprint})
}

func (p *Panel) deleteNextKey(w http.ResponseWriter, r *http.Request, s *session) {
	fed := p.fed(w)
	if fed == nil {
		return
	}
	// Already deleted: done.
	if err := fed.DeleteNextKey(r.Context()); err != nil && !errors.Is(err, msfp.ErrNoNextKey) {
		p.fail(w, "deleting the next key", err)
		return
	}
	p.Logger.Info("the federation's next key was deleted from the server in the control panel", "by", s.logID)
	w.WriteHeader(http.StatusNoContent)
}

// engineNextKey is an exported next key as the engine reads it: one an
// Overfin before the engine exported ("overfin-next-key-1.", then the
// same seed in the same base64url) is the engine's form
// ("msfp-next-key-1."), so a copy kept offline then still changes this
// server's key.
func engineNextKey(text string) string {
	if rest, ok := strings.CutPrefix(strings.TrimSpace(text), "overfin-next-key-1."); ok {
		return "msfp-next-key-1." + rest
	}
	return text
}

// nextKeyRequest is the next key given back, as exported, when it isn't
// on the server any more; empty otherwise. WithoutNextKey, for a
// revocation: don't move to the next key (it may be stolen too).
type nextKeyRequest struct {
	NextKey        string `json:"nextKey"`
	WithoutNextKey bool   `json:"withoutNextKey"`
}

func (p *Panel) rotateKey(w http.ResponseWriter, r *http.Request, s *session) {
	fed := p.fed(w)
	if fed == nil {
		return
	}
	var req nextKeyRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request"})
		return
	}
	if err := fed.Rotate(r.Context(), engineNextKey(req.NextKey)); err != nil {
		p.nextKeyProblem(w, "changing the federation key", err)
		return
	}
	p.Logger.Info("the federation key was changed in the control panel", "by", s.logID)
	w.WriteHeader(http.StatusNoContent)
}

func (p *Panel) revokeKey(w http.ResponseWriter, r *http.Request, s *session) {
	fed := p.fed(w)
	if fed == nil {
		return
	}
	var req nextKeyRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request"})
		return
	}
	rotated, err := fed.KeyStolen(r.Context(), engineNextKey(req.NextKey), req.WithoutNextKey)
	if errors.Is(err, msfp.ErrNoNextKey) && !req.WithoutNextKey {
		// No next key here, none given: revoked anyway, and every pairing
		// ends (the panel says so; nothing was revoked yet).
		rotated, err = fed.KeyStolen(r.Context(), "", true)
	}
	if err != nil {
		p.nextKeyProblem(w, "revoking the federation key", err)
		return
	}
	p.Logger.Warn("the federation key was revoked in the control panel", "rotated", rotated, "by", s.logID)
	writeJSON(w, http.StatusOK, map[string]bool{"rotated": rotated})
}

// nextKeyProblem explains why the next key couldn't be used.
func (p *Panel) nextKeyProblem(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, msfp.ErrNoNextKey):
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request",
			"detail": "The next key isn't on this server any more: paste the copy you exported."})
	case errors.Is(err, msfp.ErrNotNextKey):
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request",
			"detail": "That isn't this server's next key: paste the whole text you exported, starting with msfp-next-key-1. (or overfin-next-key-1., for a copy exported before)."})
	case errors.Is(err, msfp.ErrRotationUnderWay):
		writeJSON(w, http.StatusConflict, map[string]string{"type": "key-busy",
			"detail": "Another change of this server's key is running now: try again once it's over."})
	default:
		p.fail(w, what, err)
	}
}

func (p *Panel) confirmPeerKey(w http.ResponseWriter, r *http.Request, s *session) {
	fed := p.fed(w)
	if fed == nil {
		return
	}
	peer, err := fed.ConfirmRotation(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, msfp.ErrNotFound):
		notFound(w, "No such server")
		return
	case errors.Is(err, msfp.ErrNoPending), errors.Is(err, msfp.ErrSuspended):
		writeJSON(w, http.StatusConflict, map[string]string{"type": "no-pending-key", "detail": "That server has no new key waiting for you."})
		return
	case errors.Is(err, msfp.ErrKeyTaken):
		writeJSON(w, http.StatusConflict, map[string]string{"type": "key-taken",
			"detail": "That server's new key is another server's, or one whose pairing ended: it can't be confirmed."})
		return
	case err != nil:
		p.fail(w, "confirming a peer's new key", err)
		return
	}
	p.Logger.Info("a peer's new federation key was confirmed in the control panel", "peer", peer.ID, "by", s.logID)
	writeJSON(w, http.StatusOK, peerOf(peer))
}

func (p *Panel) dismissRefusedKey(w http.ResponseWriter, r *http.Request, _ *session) {
	fed := p.fed(w)
	if fed == nil {
		return
	}
	switch err := fed.DismissRefused(r.Context(), r.PathValue("id")); {
	case errors.Is(err, msfp.ErrNotFound):
		notFound(w, "No such server")
		return
	case err != nil:
		p.fail(w, "dismissing a refused key", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
