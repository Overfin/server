package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/overfin/server/internal/federation"
	"github.com/overfin/server/internal/msfp"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/textclean"
)

// maxLabel is how long our name for a taken library's entry may be.
const maxLabel = 64

// Federation: pairing this server with others (ROADMAP.md milestone 6,
// SECURITY.md rules 94 and 96): pairing, then sharing library by library.

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
	// WantsGone counts the libraries we want of it that it no longer
	// offered at our last reading (it stopped, or changed its IDs): to
	// choose again.
	WantsGone int `json:"wantsGone,omitempty"`
	// Unusable: its key, as stored, isn't one MSFP accepts (of small
	// order, say): no request from it, nor answer, is accepted. An honest
	// server's key never is.
	Unusable bool `json:"unusable,omitempty"`
	// OtherVersion: its latest hello lists no version of MSFP's core this
	// server speaks: the two can't talk (MSFP core section 10.3).
	OtherVersion bool `json:"otherVersion,omitempty"`

	// Its keys (MSFP core section 12). Commitment is where its next key
	// commitment came from ("pair", "rotation", "hello" for trust on
	// first use), "" for none: a change of its key then waits for you.
	Commitment string `json:"commitment"`
	// Pending is a change of its key waiting for you to confirm: the new
	// key's fingerprint, and the statement's date.
	Pending *pendingJSON `json:"pending,omitempty"`
	// SuspendedAt is when it revoked its key.
	SuspendedAt string `json:"suspendedAt,omitempty"`
	// Refused is a change of its key to a key it hadn't committed to.
	Refused *refusedJSON `json:"refused,omitempty"`
	// OurKey is what it knows of ours: "" our current key, "old" not
	// moved yet, "lost" didn't move in time; RotationAnswer, its answer
	// to our rotate ("pending", "refused").
	OurKey         string `json:"ourKey,omitempty"`
	RotationAnswer string `json:"rotationAnswer,omitempty"`

	// Addresses (MSFP core section 13). NotMoved: its requests are still
	// signed for this server's former address; MoveLate, for some days.
	// Moving is an address it says it moved to, not confirmed yet.
	NotMoved bool        `json:"notMoved,omitempty"`
	MoveLate bool        `json:"moveLate,omitempty"`
	Moving   *movingJSON `json:"moving,omitempty"`
}

// movingJSON is a peer's new address, being checked: since when that
// fails, and whether that's long enough to warn about.
type movingJSON struct {
	Address string `json:"address"`
	Failing string `json:"failing,omitempty"`
	Warn    bool   `json:"warn,omitempty"`
}

type pendingJSON struct {
	Fingerprint string `json:"fingerprint"`
	Date        string `json:"date"`
}

type refusedJSON struct {
	Fingerprint string `json:"fingerprint"`
	At          string `json:"at"`
}

func peerOf(p *store.Peer) peerJSON {
	j := peerJSON{ID: p.ID, Name: p.Name, Address: p.Address, State: p.State,
		Fingerprint: msfp.Fingerprint(p.PublicKey), CreatedAt: p.Created.Format(time.RFC3339),
		Unusable: !msfp.ValidKey(p.PublicKey), OtherVersion: !federation.SpeaksCore(p)}
	if p.Paired != nil {
		j.PairedAt = p.Paired.Format(time.RFC3339)
	}
	if p.Seen != nil {
		j.SeenAt = p.Seen.Format(time.RFC3339)
	}
	j.Commitment, j.OurKey, j.RotationAnswer = p.CommitmentFrom, p.OurKey, p.RotationAnswer
	if p.NextCommitment == nil {
		j.Commitment = ""
	}
	if p.Pending != nil {
		j.Pending = &pendingJSON{Fingerprint: msfp.Fingerprint(p.Pending.New), Date: time.Unix(p.Pending.Date, 0).UTC().Format(time.RFC3339)}
	}
	if p.Suspended != nil {
		j.SuspendedAt = p.Suspended.Format(time.RFC3339)
	}
	if p.RefusedKey != nil && p.RefusedAt != nil {
		j.Refused = &refusedJSON{Fingerprint: msfp.Fingerprint(p.RefusedKey), At: p.RefusedAt.Format(time.RFC3339)}
	}
	return j
}

// peerCounted is peerOf with what's offered and wanted, and its titles
// (counts, PeerTitleCounts), as the list shows them.
func (p *Panel) peerCounted(ctx context.Context, pe *store.Peer, counts map[string]int) (peerJSON, error) {
	j := peerOf(pe)
	offers, err := p.Store.PeerOffers(ctx, pe.ID)
	if err != nil {
		return j, err
	}
	wants, err := p.Store.PeerWants(ctx, pe.ID)
	if err != nil {
		return j, err
	}
	j.Offers, j.Wants, j.Titles = len(offers), len(wants), counts[pe.ID]
	for _, w := range wants {
		if w.Gone {
			j.WantsGone++
		}
	}
	return j, nil
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
			"detail": "Federation needs the site's https address: set site.url (or an HTTPS domain) in the configuration."})
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
	now := time.Now()
	notMoved, others := 0, 0
	for _, pe := range peers {
		j, err := p.peerCounted(ctx, pe, counts)
		if err != nil {
			p.fail(w, "reading offers and wants", err)
			return
		}
		j.NotMoved, j.MoveLate = s.NotMoved(pe)
		if j.NotMoved {
			notMoved++
		}
		if pe.State != store.PeerGone {
			others++
		}
		if m := pe.Move; m != nil {
			j.Moving = &movingJSON{Address: m.Address, Warn: m.Warn(now)}
			if !m.Failing.IsZero() {
				j.Moving.Failing = m.Failing.Format(time.RFC3339)
			}
		}
		outPeers = append(outPeers, j)
	}
	nextFingerprint, nextOnServer, err := s.NextKey(ctx)
	if err != nil {
		p.fail(w, "reading the next key", err)
		return
	}
	out := map[string]any{"available": true, "address": s.Address,
		// The test setting federation.test_plain_http, on: said so.
		"testPlainHTTP": strings.HasPrefix(s.Address, "http://"),
		"fingerprint":   msfp.Fingerprint(s.Keys.Current().Public()), "codes": outCodes, "peers": outPeers,
		"nextKey": map[string]any{"fingerprint": nextFingerprint, "onServer": nextOnServer}}
	// A change of this server's key under way: since when, until when the
	// old key is kept.
	if _, since, ok := s.Keys.Old(); ok {
		out["rotation"] = map[string]string{"since": since.Format(time.RFC3339),
			"until": since.Add(federation.KeyOverlap).Format(time.RFC3339)}
	}
	// A change of this server's address under way: from which, since
	// when, until when the former is accepted, and how many peers moved.
	if former, since, ok := s.Moving(); ok {
		out["move"] = map[string]any{"former": former, "since": since.Format(time.RFC3339),
			"until": since.Add(msfp.AddressOverlap).Format(time.RFC3339), "moved": others - notMoved, "of": others}
	}
	writeJSON(w, http.StatusOK, out)
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
	c, err := msfp.ParseCode(req.Code)
	if err != nil {
		p.pairingProblem(w, err)
		return
	}
	if msfp.SameKey(c.Key, fed.Keys.Current().Public()) {
		p.pairingProblem(w, federation.ErrOwnCode)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"address": c.Address, "fingerprint": msfp.Fingerprint(c.Key),
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
	unreachable := err != nil
	counts, err := p.Store.PeerTitleCounts(r.Context())
	if err != nil {
		p.fail(w, "counting peers' titles", err)
		return
	}
	pj, err := p.peerCounted(r.Context(), peer, counts)
	if err != nil {
		p.fail(w, "reading offers and wants", err)
		return
	}
	j := map[string]any{"peer": pj}
	if unreachable {
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
	case errors.Is(err, msfp.ErrCode):
		detail = "That isn't a pairing code. Paste the whole code, starting with msfp-pair-1."
	case errors.Is(err, msfp.ErrPlainHTTP):
		detail = "That code is for a server at a plain http:// address. Servers pair over HTTPS only: ask its operator to give it an https address, then a new code."
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
		// Label is our name for a wanted one's entry in the site's menu,
		// "" for Shown, the one made of the other server's names.
		Label string `json:"label,omitempty"`
		Shown string `json:"shown,omitempty"`
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
		// What it says we may use of it and what it calls itself, read
		// again with its libraries.
		if again, err := p.Store.PeerByID(ctx, peer.ID); err == nil {
			peer = again
		}
	}
	label := func(id string) string {
		for _, w := range wants {
			if w.ID == id {
				return w.Label
			}
		}
		return ""
	}
	if reachable {
		for _, l := range theirs {
			outTheirs = append(outTheirs, lib{ID: l.ID, Name: l.Name, Kind: l.Kind, Picked: wanted(l.ID),
				Label: label(l.ID), Shown: l.Name + " (" + peer.Name + ")"})
		}
	}
	for _, w := range wants {
		if !slices.ContainsFunc(theirs, func(l store.PeerLibrary) bool { return l.ID == w.ID }) {
			outTheirs = append(outTheirs, lib{ID: w.ID, Name: w.Name, Kind: w.Kind, Picked: true, Gone: reachable || w.Gone,
				Label: w.Label, Shown: w.Name + " (" + peer.Name + ")"})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ours": outOurs, "theirs": outTheirs, "reachable": reachable,
		"notSupported": errors.Is(err, federation.ErrNotSupported), "limits": peer.Limits, "theirLimits": peer.Theirs})
}

func (p *Panel) setSharing(w http.ResponseWriter, r *http.Request, s *session) {
	fed := p.fed(w)
	if fed == nil {
		return
	}
	var req struct {
		Offer  []string          `json:"offer"`
		Want   []string          `json:"want"`
		Limits *store.PeerLimits `json:"limits"`
		// Labels are our names for wanted libraries' entries in the site's
		// menu, by the other server's library ID ("" for its own names).
		Labels map[string]string `json:"labels"`
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
	if req.Limits != nil {
		if err := federation.ValidLimits(*req.Limits); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request",
				"detail": "Streams and transcodes go from 0 to 50, with no more transcodes than streams; the bitrate cap from 0 (none) to 1,000,000 kbps."})
			return
		}
		if err := p.Store.SetPeerLimits(ctx, peer.ID, *req.Limits); err != nil {
			p.fail(w, "setting a peer's limits", err)
			return
		}
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
			if errors.Is(err, federation.ErrNotSupported) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request",
					"detail": "What you offer is saved, but what you want couldn't be: the other server doesn't share libraries with this one (it speaks another version of the protocol, or hasn't said which yet). Try Check, then again."})
				return
			}
			p.fail(w, "setting wants", err)
			return
		}
	}
	for id, l := range req.Labels {
		err := p.Store.LabelPeerWant(ctx, peer.ID, id, textclean.Plain(l, maxLabel))
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			p.fail(w, "naming a library from a peer", err)
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
