package admin

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/overfin/server/internal/federation"
	"github.com/overfin/server/internal/store"
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
	case errors.Is(err, federation.ErrNoNextKey):
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
	if err := fed.DeleteNextKey(r.Context()); err != nil {
		p.fail(w, "deleting the next key", err)
		return
	}
	p.Logger.Info("the federation's next key was deleted from the server in the control panel", "by", s.logID)
	w.WriteHeader(http.StatusNoContent)
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
	if err := fed.Rotate(r.Context(), req.NextKey); err != nil {
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
	rotated, err := fed.Revoke(r.Context(), req.NextKey, req.WithoutNextKey)
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
	case errors.Is(err, federation.ErrNoNextKey):
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request",
			"detail": "The next key isn't on this server any more: paste the copy you exported."})
	case errors.Is(err, federation.ErrNotNextKey):
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request",
			"detail": "That isn't this server's next key: paste the whole text you exported, starting with overfin-next-key-1."})
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
	case errors.Is(err, store.ErrNotFound):
		notFound(w, "No such server")
		return
	case errors.Is(err, federation.ErrNoPending), errors.Is(err, federation.ErrSuspended):
		writeJSON(w, http.StatusConflict, map[string]string{"type": "no-pending-key", "detail": "That server has no new key waiting for you."})
		return
	case errors.Is(err, federation.ErrKeyTaken):
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
	case errors.Is(err, store.ErrNotFound):
		notFound(w, "No such server")
		return
	case err != nil:
		p.fail(w, "dismissing a refused key", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
