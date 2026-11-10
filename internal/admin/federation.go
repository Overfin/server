package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
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
	// Offers and Wants count the libraries we offer it and want of it;
	// Titles, the films and series it holds in those we want.
	Offers int `json:"offers"`
	Wants  int `json:"wants"`
	Titles int `json:"titles"`
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
	counts, err := p.Store.PeerTitleCounts(ctx)
	if err != nil {
		p.fail(w, "counting peers' titles", err)
		return
	}
	outPeers := []peerJSON{}
	for _, pe := range peers {
		j := peerOf(pe)
		offers, err := p.Store.PeerOffers(ctx, pe.ID)
		if err != nil {
			p.fail(w, "reading offers", err)
			return
		}
		wants, err := p.Store.PeerWants(ctx, pe.ID)
		if err != nil {
			p.fail(w, "reading wants", err)
			return
		}
		j.Offers, j.Wants, j.Titles = len(offers), len(wants), counts[pe.ID]
		outPeers = append(outPeers, j)
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

// sharing is what we offer a paired server and what we want of its
// libraries: ours, each offered or not; theirs as it offers them now (or,
// when it can't be reached, only those we want, as we last saw them).
func (p *Panel) sharing(w http.ResponseWriter, r *http.Request, _ *session) {
	fed := p.fed(w)
	if fed == nil {
		return
	}
	ctx := r.Context()
	peer := p.pairedPeer(w, r)
	if peer == nil {
		return
	}
	ours, err := fed.OurLibraries(ctx)
	if err != nil {
		p.fail(w, "listing libraries", err)
		return
	}
	offered, err := p.Store.PeerOffers(ctx, peer.ID)
	if err != nil {
		p.fail(w, "reading offers", err)
		return
	}
	wants, err := p.Store.PeerWants(ctx, peer.ID)
	if err != nil {
		p.fail(w, "reading wants", err)
		return
	}
	type lib struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Kind   string `json:"kind"`
		Picked bool   `json:"picked"`
		// Gone: we want it, and the other server no longer offers it.
		Gone bool `json:"gone,omitempty"`
	}
	outOurs := []lib{}
	for _, l := range ours {
		outOurs = append(outOurs, lib{ID: l.ID, Name: l.Name, Kind: l.Kind, Picked: slices.Contains(offered, l.ID)})
	}
	wanted := func(id string) bool {
		return slices.ContainsFunc(wants, func(w store.PeerLibrary) bool { return w.ID == id })
	}
	outTheirs := []lib{}
	theirs, err := fed.TheirLibraries(ctx, peer)
	reachable := err == nil
	if reachable {
		for _, l := range theirs {
			outTheirs = append(outTheirs, lib{ID: l.ID, Name: l.Name, Kind: l.Kind, Picked: wanted(l.ID)})
		}
	}
	for _, w := range wants {
		if !slices.ContainsFunc(theirs, func(l store.PeerLibrary) bool { return l.ID == w.ID }) {
			outTheirs = append(outTheirs, lib{ID: w.ID, Name: w.Name, Kind: w.Kind, Picked: true, Gone: reachable})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ours": outOurs, "theirs": outTheirs, "reachable": reachable})
}

func (p *Panel) setSharing(w http.ResponseWriter, r *http.Request, s *session) {
	fed := p.fed(w)
	if fed == nil {
		return
	}
	var req struct {
		Offer []string `json:"offer"`
		Want  []string `json:"want"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request"})
		return
	}
	ctx := r.Context()
	peer := p.pairedPeer(w, r)
	if peer == nil {
		return
	}
	if err := fed.SetOffers(ctx, peer, req.Offer); err != nil {
		p.fail(w, "setting offers", err)
		return
	}
	// What we want changes only when it did: picking needs the other
	// server's list, so it must be reachable then.
	wants, err := p.Store.PeerWants(ctx, peer.ID)
	if err != nil {
		p.fail(w, "reading wants", err)
		return
	}
	now := []string{}
	for _, w := range wants {
		now = append(now, w.ID)
	}
	slices.Sort(now)
	asked := slices.Clone(req.Want)
	slices.Sort(asked)
	asked = slices.Compact(asked)
	if !slices.Equal(now, asked) {
		err := fed.SetWants(ctx, peer, req.Want)
		if err == nil {
			// Its titles, for what we want now.
			fed.SyncSoon(peer)
		}
		if err != nil {
			if errors.Is(err, federation.ErrUnreachable) || errors.Is(err, federation.ErrUnknown) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request",
					"detail": "What you offer is saved, but what you want couldn't be: the other server can't be reached now. Try again later."})
				return
			}
			p.fail(w, "setting wants", err)
			return
		}
	}
	p.Logger.Info("sharing with a peer changed in the control panel", "peer", peer.ID, "offers", len(req.Offer), "wants", len(req.Want), "by", s.logID)
	w.WriteHeader(http.StatusNoContent)
}

// pairedPeer is the paired server the request names, or nil after
// answering.
func (p *Panel) pairedPeer(w http.ResponseWriter, r *http.Request) *store.Peer {
	peer, err := p.Store.PeerByID(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		notFound(w, "No such server")
		return nil
	case err != nil:
		p.fail(w, "reading a peer", err)
		return nil
	case peer.State != store.PeerPaired:
		writeJSON(w, http.StatusConflict, map[string]string{"type": "not-paired", "detail": "Sharing starts once both operators have accepted."})
		return nil
	}
	return peer
}
