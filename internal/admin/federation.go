package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/overfin/server/internal/federation"
	"github.com/overfin/server/internal/store"
)

// Federation: pairing this server with others (ROADMAP.md milestone 6,
// SECURITY.md rule 65). Pairing only, so far: nothing is shared yet.

type peerJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Address     string `json:"address"`
	State       string `json:"state"`
	Fingerprint string `json:"fingerprint"`
	CreatedAt   string `json:"createdAt"`
	PairedAt    string `json:"pairedAt,omitempty"`
	SeenAt      string `json:"seenAt,omitempty"`
}

func peerOf(p *store.Peer) peerJSON {
	j := peerJSON{ID: p.ID, Name: p.Name, Address: p.Address, State: p.State,
		Fingerprint: federation.Fingerprint(p.PublicKey), CreatedAt: p.Created.Format(time.RFC3339)}
	if p.Paired != nil {
		j.PairedAt = p.Paired.Format(time.RFC3339)
	}
	if p.Seen != nil {
		j.SeenAt = p.Seen.Format(time.RFC3339)
	}
	return j
}

type codeJSON struct {
	ID        string `json:"id"`
	Note      string `json:"note"`
	CreatedAt string `json:"createdAt"`
	ExpiresAt string `json:"expiresAt"`
	// Code is the code itself, only in the answer that makes it.
	Code string `json:"code,omitempty"`
}

func codeOf(c store.PairingCode) codeJSON {
	return codeJSON{ID: c.ID, Note: c.Note, CreatedAt: c.Created.Format(time.RFC3339), ExpiresAt: c.Expires.Format(time.RFC3339)}
}

// fed is federation now, or nil after answering that it's unavailable.
func (p *Panel) fed(w http.ResponseWriter) *federation.Service {
	var s *federation.Service
	if p.Federation != nil {
		s = p.Federation()
	}
	if s == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"type": "unavailable",
			"detail": "Federation needs the site's address: set site.url (or an HTTPS domain) in the configuration."})
	}
	return s
}

func (p *Panel) federation(w http.ResponseWriter, r *http.Request, _ *session) {
	var s *federation.Service
	if p.Federation != nil {
		s = p.Federation()
	}
	if s == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	ctx := r.Context()
	codes, err := p.Store.PairingCodes(ctx)
	if err != nil {
		p.fail(w, "listing pairing codes", err)
		return
	}
	peers, err := p.Store.Peers(ctx)
	if err != nil {
		p.fail(w, "listing peers", err)
		return
	}
	// Those whose operator we're waiting for: asked, in the background.
	s.CheckSoon(peers)
	outCodes := []codeJSON{}
	for _, c := range codes {
		outCodes = append(outCodes, codeOf(c))
	}
	outPeers := []peerJSON{}
	for _, pe := range peers {
		outPeers = append(outPeers, peerOf(pe))
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "address": s.Address,
		"fingerprint": federation.Fingerprint(s.Key.Public()), "codes": outCodes, "peers": outPeers})
}

func (p *Panel) createPairingCode(w http.ResponseWriter, r *http.Request, s *session) {
	fed := p.fed(w)
	if fed == nil {
		return
	}
	var req struct {
		Note string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request"})
		return
	}
	text, c, err := fed.NewCode(r.Context(), req.Note)
	if err != nil {
		p.fail(w, "making a pairing code", err)
		return
	}
	p.Logger.Info("a pairing code was made in the control panel", "code", c.ID, "by", s.logID)
	j := codeOf(*c)
	j.Code = text
	writeJSON(w, http.StatusCreated, j)
}

func (p *Panel) deletePairingCode(w http.ResponseWriter, r *http.Request, s *session) {
	switch err := p.Store.DeletePairingCode(r.Context(), r.PathValue("id")); {
	case errors.Is(err, store.ErrNotFound):
		notFound(w, "No such pairing code")
		return
	case err != nil:
		p.fail(w, "withdrawing a pairing code", err)
		return
	}
	p.Logger.Info("a pairing code was withdrawn in the control panel", "code", r.PathValue("id"), "by", s.logID)
	w.WriteHeader(http.StatusNoContent)
}

// previewPairingCode reads a pasted code, before it's used: which server
// it's for, and its fingerprint, to compare with that server's operator.
func (p *Panel) previewPairingCode(w http.ResponseWriter, r *http.Request, _ *session) {
	fed := p.fed(w)
	if fed == nil {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request"})
		return
	}
	c, err := federation.ParseCode(req.Code)
	if err != nil {
		p.pairingProblem(w, err)
		return
	}
	if federation.SameKey(c.Key, fed.Key.Public()) {
		p.pairingProblem(w, federation.ErrOwnCode)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"address": c.Address, "fingerprint": federation.Fingerprint(c.Key),
		"expiresAt": c.Expires.UTC().Format(time.RFC3339), "expired": !c.Expires.After(time.Now())})
}

func (p *Panel) usePairingCode(w http.ResponseWriter, r *http.Request, s *session) {
	fed := p.fed(w)
	if fed == nil {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request"})
		return
	}
	peer, err := fed.UseCode(r.Context(), req.Code)
	if err != nil {
		p.pairingProblem(w, err)
		return
	}
	p.Logger.Info("a pairing code was used in the control panel", "peer", peer.ID, "by", s.logID)
	writeJSON(w, http.StatusCreated, peerOf(peer))
}

func (p *Panel) acceptPeer(w http.ResponseWriter, r *http.Request, s *session) {
	fed := p.fed(w)
	if fed == nil {
		return
	}
	peer, err := fed.Accept(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		notFound(w, "No server waiting for you with that ID")
		return
	case peer == nil:
		p.fail(w, "accepting a peer", err)
		return
	}
	// The other server may be unreachable for now: it's paired anyway,
	// and learns it when it next asks.
	p.Logger.Info("a peer was accepted in the control panel", "peer", peer.ID, "by", s.logID)
	writeJSON(w, http.StatusOK, peerOf(peer))
}

func (p *Panel) checkPeer(w http.ResponseWriter, r *http.Request, _ *session) {
	fed := p.fed(w)
	if fed == nil {
		return
	}
	peer, err := p.Store.PeerByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, "No such server")
		return
	}
	if err != nil {
		p.fail(w, "reading a peer", err)
		return
	}
	peer, err = fed.Check(r.Context(), peer)
	if peer == nil {
		p.fail(w, "checking a peer", err)
		return
	}
	j := map[string]any{"peer": peerOf(peer)}
	if err != nil {
		j["unreachable"] = true
	}
	writeJSON(w, http.StatusOK, j)
}

func (p *Panel) endPeer(w http.ResponseWriter, r *http.Request, s *session) {
	fed := p.fed(w)
	if fed == nil {
		return
	}
	switch err := fed.End(r.Context(), r.PathValue("id")); {
	case errors.Is(err, store.ErrNotFound):
		notFound(w, "No such server")
		return
	case err != nil:
		p.fail(w, "ending a pairing", err)
		return
	}
	p.Logger.Info("a pairing was ended in the control panel", "peer", r.PathValue("id"), "by", s.logID)
	w.WriteHeader(http.StatusNoContent)
}

// pairingProblem explains why a code couldn't be used.
func (p *Panel) pairingProblem(w http.ResponseWriter, err error) {
	detail := ""
	switch {
	case errors.Is(err, federation.ErrCode):
		detail = "That isn't a pairing code. Paste the whole code, starting with overfin-pair-1."
	case errors.Is(err, federation.ErrOwnCode):
		detail = "That's this server's own code: send it to the other server's operator instead."
	case errors.Is(err, store.ErrPeerExists):
		detail = "That server is already in the list."
	case errors.Is(err, federation.ErrCodeRefused):
		detail = "The other server refused the code: it was used, withdrawn, or is more than a day old. Ask for a new one."
	case errors.Is(err, federation.ErrUnreachable):
		detail = "The other server couldn't be reached, or didn't answer as the server the code is from. Check that it's running and reachable."
	default:
		p.fail(w, "using a pairing code", err)
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": detail})
}
