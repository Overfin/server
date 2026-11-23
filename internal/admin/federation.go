package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/msfp-io/msfp-go"
	"github.com/msfp-io/msfp-go/wire"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/textclean"
)

// maxLabel is how long our name for a taken library's entry may be.
const maxLabel = 64

// keyOverlap is how long a change of this server's key lasts at most:
// the old key is kept that long for peers that haven't moved (MSFP core
// section 12.4).
const keyOverlap = 30 * 24 * time.Hour

// Federation: pairing this server with others (ROADMAP.md milestone 6,
// SECURITY.md rules 94 and 96): pairing, then sharing library by library.
// The MSFP engine (msfp) does it all; the panel calls its actions and
// shows its views, and reads Overfin's own store only for what is
// Overfin's (the menu labels of the libraries taken, the titles counted).

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
	// OtherVersion: its latest hello lists no version of MSFP's core this
	// server speaks: the two can't talk (MSFP core section 10.3).
	OtherVersion bool `json:"otherVersion,omitempty"`

	// Its keys (MSFP core section 12). Commitment is where its next key
	// commitment came from ("pair", "rotation"), "" for none: a change of
	// its key then waits for you.
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

	// Servers that can't be called (MSFP core section 7.5). Inbound is
	// "no" when it can't be (it can watch what this server shares with
	// it, and shares nothing with it), "confirming" when it says it can
	// again and its address is being checked; left out when it can be.
	// CallsFailed: its last answer said this server's last calls to it
	// couldn't connect.
	Inbound     string `json:"inbound,omitempty"`
	CallsFailed bool   `json:"callsFailed,omitempty"`

	// Relations (MSFP core section 7.7). Groups are the IDs of the groups
	// this pairing was made or kept through; Individual, whether the two
	// are also paired directly (both want it); Asked, this server asked to
	// pair directly and waits; Asks, the other server asks, waiting for
	// you; KeepChoice, its group was dissolved and you choose whether to
	// stay paired.
	Groups []string `json:"groups,omitempty"`
	// JoiningGroup: it used one of this server's invites to a group and
	// waits for you: a request to join that group, shown there.
	JoiningGroup string `json:"joiningGroup,omitempty"`
	Individual   bool   `json:"individual"`
	Asked        bool   `json:"asked,omitempty"`
	Asks         bool   `json:"asks,omitempty"`
	KeepChoice   bool   `json:"keepChoice,omitempty"`
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

// limitsJSON is what a pairing may use (MSFP playback section 5), as the
// panel shows and sets it: always these three numbers.
type limitsJSON struct {
	Streams     int `json:"streams"`
	Transcodes  int `json:"transcodes"`
	BitrateKbps int `json:"bitrateKbps"`
}

func limitsOf(l msfp.Limits) limitsJSON {
	return limitsJSON{Streams: l.Streams, Transcodes: l.Transcodes, BitrateKbps: l.BitrateKbps}
}

// stamp is t for the panel, "" for the zero time (never).
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func peerOf(v msfp.PeerView) peerJSON {
	j := peerJSON{ID: v.ID, Name: v.Name, Address: v.Address, State: string(v.State),
		Fingerprint: v.Fingerprint, CreatedAt: stamp(v.Created), PairedAt: stamp(v.Paired), SeenAt: stamp(v.Seen),
		Offers: v.Offers, Wants: v.Wants, WantsGone: v.WantsGone, OtherVersion: v.Core < 0,
		Commitment: v.CommitmentFrom, SuspendedAt: stamp(v.Suspended), OurKey: v.OurKey, RotationAnswer: v.RotationAnswer,
		NotMoved: v.NotMoved, MoveLate: v.MoveLate, CallsFailed: v.CallsFailed, Groups: v.Groups,
		Individual: v.Individual, Asked: v.IndividualAsked, Asks: v.IndividualAsks, KeepChoice: v.KeepChoice,
		JoiningGroup: v.JoiningGroup}
	if v.Pending != nil {
		j.Pending = &pendingJSON{Fingerprint: v.Pending.Fingerprint, Date: stamp(v.Pending.At)}
	}
	if v.Refused != nil {
		j.Refused = &refusedJSON{Fingerprint: v.Refused.Fingerprint, At: stamp(v.Refused.At)}
	}
	if m := v.Moving; m != nil {
		j.Moving = &movingJSON{Address: m.Address, Failing: stamp(m.FailingSince), Warn: m.Warn}
	}
	if v.Inbound != msfp.InboundYes && v.Inbound != "" {
		j.Inbound = string(v.Inbound)
	}
	return j
}

// peerCounted is peerOf with the titles it holds in what we want of it
// (counts, PeerTitleCounts), as the list shows it.
func peerCounted(v msfp.PeerView, counts map[string]int) peerJSON {
	j := peerOf(v)
	j.Titles = counts[v.ID]
	return j
}

type codeJSON struct {
	ID        string `json:"id"`
	Note      string `json:"note"`
	CreatedAt string `json:"createdAt"`
	ExpiresAt string `json:"expiresAt"`
	// Uses is how many more servers may use it; Group the group it
	// invites to ("" for none).
	Uses  int    `json:"uses"`
	Group string `json:"group,omitempty"`
	// Code is the code itself, only in the answer that makes it;
	// MailedTo, where it was emailed then, or MailProblem, why it wasn't.
	Code        string `json:"code,omitempty"`
	MailedTo    string `json:"mailedTo,omitempty"`
	MailProblem string `json:"mailProblem,omitempty"`
}

func codeOf(c msfp.CodeView) codeJSON {
	return codeJSON{ID: c.ID, Note: c.Note, CreatedAt: stamp(c.Created), ExpiresAt: stamp(c.Expires), Uses: c.Uses, Group: c.Group}
}

// CodeMailer emails a federation pairing code to another server's
// operator.
type CodeMailer interface {
	// CanSendMail says whether emails can be sent (the SMTP server set).
	CanSendMail() bool
	// SendPairingCode emails code, which invites to group ("" for a
	// pairing) and stops working at expires. Its error says what's wrong
	// in plain words.
	SendPairingCode(ctx context.Context, to, code, group string, expires time.Time) error
}

// engine is federation now, nil without it.
func (p *Panel) engine() *msfp.Engine {
	if p.Engine == nil {
		return nil
	}
	return p.Engine()
}

// fed is federation now, or nil after answering that it's unavailable.
func (p *Panel) fed(w http.ResponseWriter) *msfp.Engine {
	e := p.engine()
	if e == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"type": "unavailable",
			"detail": "Federation needs the site's https address: set site.url (or an HTTPS domain) in the configuration."})
	}
	return e
}

func (p *Panel) federation(w http.ResponseWriter, r *http.Request, _ *session) {
	eng := p.engine()
	ctx := r.Context()
	if eng == nil {
		// The setting shows anyway: a server that can't be called needs
		// no site address.
		uncallable := false
		if p.Uncallable != nil {
			on, err := p.Uncallable.FederationUncallable(ctx)
			if err != nil {
				p.fail(w, "reading whether this server can be called", err)
				return
			}
			uncallable = on
		}
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "uncallable": uncallable, "canSet": p.Uncallable != nil})
		return
	}
	sv, err := eng.Server(ctx)
	if err != nil {
		p.fail(w, "reading this server's federation", err)
		return
	}
	codes, err := eng.Codes(ctx)
	if err != nil {
		p.fail(w, "listing pairing codes", err)
		return
	}
	peers, err := eng.Peers(ctx)
	if err != nil {
		p.fail(w, "listing peers", err)
		return
	}
	// Those whose operator we're waiting for: asked, in the background.
	eng.PanelOpen()
	counts, err := p.Store.PeerTitleCounts(ctx)
	if err != nil {
		p.fail(w, "counting peers' titles", err)
		return
	}
	outCodes := []codeJSON{}
	for _, c := range codes {
		outCodes = append(outCodes, codeOf(c))
	}
	outPeers := []peerJSON{}
	for _, v := range peers {
		outPeers = append(outPeers, peerCounted(v, counts))
	}
	groups, err := p.groups(r, eng, peers)
	if err != nil {
		p.fail(w, "listing groups", err)
		return
	}
	out := map[string]any{"available": true, "address": sv.Address, "uncallable": sv.Uncallable, "canSet": p.Uncallable != nil,
		// The test setting federation.test_plain_http, on: said so.
		"testPlainHTTP": strings.HasPrefix(sv.Address, "http://"),
		"fingerprint":   sv.Fingerprint, "codes": outCodes, "peers": outPeers, "groups": groups,
		// Codes can be emailed.
		"canMail": p.CodeMail != nil && p.CodeMail.CanSendMail(),
		"nextKey": map[string]any{"fingerprint": sv.NextKey, "onServer": sv.NextOnServer}}
	// A change of this server's key under way: since when, until when the
	// old key is kept.
	if !sv.RotationSince.IsZero() {
		out["rotation"] = map[string]string{"since": stamp(sv.RotationSince), "until": stamp(sv.RotationSince.Add(keyOverlap))}
	}
	// A change of this server's address under way: from which, since
	// when, until when the former is accepted, how many peers moved, and
	// whether the new address is still waited for, not announced yet.
	if sv.Former != "" {
		out["move"] = map[string]any{"former": sv.Former, "since": stamp(sv.MovedAt),
			"until": stamp(sv.MovedAt.Add(wire.AddressOverlap)), "moved": sv.PeersMoved, "of": sv.PeersToMove,
			"waiting": sv.Waiting}
	}
	writeJSON(w, http.StatusOK, out)
}

func (p *Panel) createPairingCode(w http.ResponseWriter, r *http.Request, s *session) {
	eng := p.fed(w)
	if eng == nil {
		return
	}
	var req struct {
		Note string `json:"note"`
		// Group is a group's ID, for a code that invites to it; Uses how
		// many servers may use it (0: one); Hours how long it works (0:
		// 24).
		Group string `json:"group"`
		Uses  int    `json:"uses"`
		Hours int    `json:"hours"`
		// Email, when set, is where the code is sent too: to the other
		// server's operator.
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request"})
		return
	}
	email := strings.TrimSpace(req.Email)
	if email != "" {
		if a, err := mail.ParseAddress(email); err != nil || a.Address != email || len(email) > 254 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "That isn't an email address."})
			return
		}
		if p.CodeMail == nil || !p.CodeMail.CanSendMail() {
			writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "Email is off: set it up first (Server, Emails)."})
			return
		}
	}
	text, c, err := eng.MakeCodeWith(r.Context(), msfp.CodeOptions{Note: req.Note, Group: req.Group, Uses: req.Uses,
		Lifetime: time.Duration(req.Hours) * time.Hour})
	if errors.Is(err, msfp.ErrCodeOptions) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request",
			"detail": "A code serves 1 to 100 servers, and works from 1 hour to 30 days."})
		return
	}
	if req.Group != "" && (errors.Is(err, msfp.ErrNotFound) || errors.Is(err, msfp.ErrNoRight) || errors.Is(err, msfp.ErrNoGroups)) {
		p.groupProblem(w, "making a group's code", err)
		return
	}
	if errors.Is(err, msfp.ErrNoCodes) {
		writeJSON(w, http.StatusConflict, map[string]string{"type": "no-codes",
			"detail": "This server can't be called: it makes no codes. Ask the other server's operator for one, and use it here."})
		return
	}
	if err != nil {
		p.fail(w, "making a pairing code", err)
		return
	}
	p.Logger.Info("a pairing code was made in the control panel", "code", c.ID, "by", s.logID)
	j := codeOf(c)
	j.Code = text
	if email != "" {
		// Sent after it's made: the code is shown anyway, and a failure
		// is said beside it.
		group := ""
		if c.Group != "" {
			if g, err := p.engine().Group(r.Context(), c.Group); err == nil {
				group = g.Name
			}
		}
		if err := p.CodeMail.SendPairingCode(r.Context(), email, text, group, c.Expires); err != nil {
			// Said as the invitations' emails say it.
			j.MailProblem = err.Error()
			p.Logger.Warn("a pairing code couldn't be emailed", "code", c.ID, "error", err)
		} else {
			j.MailedTo = email
			p.Logger.Info("a pairing code was emailed from the control panel", "code", c.ID, "by", s.logID)
		}
	}
	writeJSON(w, http.StatusCreated, j)
}

func (p *Panel) deletePairingCode(w http.ResponseWriter, r *http.Request, s *session) {
	eng := p.fed(w)
	if eng == nil {
		return
	}
	switch err := eng.DeleteCode(r.Context(), r.PathValue("id")); {
	case errors.Is(err, msfp.ErrNotFound):
		notFound(w, "No such pairing code")
		return
	case err != nil:
		p.fail(w, "withdrawing a pairing code", err)
		return
	}
	p.Logger.Info("a pairing code was withdrawn in the control panel", "code", r.PathValue("id"), "by", s.logID)
	w.WriteHeader(http.StatusNoContent)
}

// setUncallable is the operator saying whether this server can't be
// called (MSFP core section 7.5): it then federates under an address that
// never resolves, makes no codes, and asks its paired servers on a
// schedule; turned off, it moves back to its site's address, and they
// call it again once they checked it answers there.
func (p *Panel) setUncallable(w http.ResponseWriter, r *http.Request, s *session) {
	if p.Uncallable == nil {
		notFound(w, "Not available here")
		return
	}
	var req struct {
		On bool `json:"on"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request"})
		return
	}
	if err := p.Uncallable.SetFederationUncallable(r.Context(), req.On); err != nil {
		p.fail(w, "changing whether this server can be called", err)
		return
	}
	p.Logger.Info("whether this server can be called was changed in the control panel", "uncallable", req.On, "by", s.logID)
	w.WriteHeader(http.StatusNoContent)
}

// previewPairingCode reads a pasted code, before it's used: which server
// it's for, and its fingerprint, to compare with that server's operator.
func (p *Panel) previewPairingCode(w http.ResponseWriter, r *http.Request, _ *session) {
	eng := p.fed(w)
	if eng == nil {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request"})
		return
	}
	c, err := eng.ReadCode(r.Context(), req.Code)
	expired := false
	if errors.Is(err, msfp.ErrCodeExpired) {
		// Shown anyway, said to be over: which server it was for still
		// helps its operator, who is asked for a new one.
		if old, perr := wire.ParseCode(req.Code); perr == nil {
			c, err, expired = msfp.CodePreview{Address: old.Address, Fingerprint: wire.Fingerprint(old.Key), Expires: old.Expires,
				GroupName: old.GroupName}, nil, true
		}
	}
	if err != nil {
		p.pairingProblem(w, err)
		return
	}
	// A group's code: this server joins the group once the code's maker
	// accepts it, then pairs with each member that accepts it.
	writeJSON(w, http.StatusOK, map[string]any{"address": c.Address, "fingerprint": c.Fingerprint,
		"expiresAt": stamp(c.Expires), "expired": expired, "groupName": c.GroupName})
}

func (p *Panel) usePairingCode(w http.ResponseWriter, r *http.Request, s *session) {
	eng := p.fed(w)
	if eng == nil {
		return
	}
	var req struct {
		Code string `json:"code"`
		// For a group's invite: what this server shares with the group,
		// and the limits it lends it, set as it joins.
		Offer  []string    `json:"offer"`
		Limits *limitsJSON `json:"limits"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request"})
		return
	}
	ctx := r.Context()
	pv, _ := eng.ReadCode(ctx, req.Code)
	peer, err := eng.RedeemCode(ctx, req.Code)
	if err != nil {
		p.pairingProblem(w, err)
		return
	}
	if pv.Group != "" {
		if req.Limits != nil {
			l := msfp.Limits{Streams: req.Limits.Streams, Transcodes: req.Limits.Transcodes, BitrateKbps: req.Limits.BitrateKbps}
			if err := eng.SetGroupPool(ctx, pv.Group, l); err != nil {
				p.Logger.Warn("federation: the limits for a group being joined couldn't be set", "group", pv.Group, "error", err)
			}
		}
		if err := eng.SetGroupOffers(ctx, pv.Group, req.Offer); err != nil {
			p.Logger.Warn("federation: what is shared with a group being joined couldn't be set", "group", pv.Group, "error", err)
		}
	}
	p.Logger.Info("a pairing code was used in the control panel", "peer", peer.ID, "by", s.logID)
	writeJSON(w, http.StatusCreated, peerOf(peer))
}

func (p *Panel) acceptPeer(w http.ResponseWriter, r *http.Request, s *session) {
	eng := p.fed(w)
	if eng == nil {
		return
	}
	// The other server may be unreachable for now: it's paired anyway,
	// and learns it when it next asks.
	peer, err := eng.Accept(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, msfp.ErrNotFound), errors.Is(err, msfp.ErrNotWaiting):
		notFound(w, "No server waiting for you with that ID")
		return
	case err != nil:
		p.fail(w, "accepting a peer", err)
		return
	}
	p.Logger.Info("a peer was accepted in the control panel", "peer", peer.ID, "by", s.logID)
	writeJSON(w, http.StatusOK, peerOf(peer))
}

// unreachable says whether err is a peer that couldn't be reached now,
// or wouldn't answer: no answer, a bad one, or asked to wait.
func unreachable(err error) bool {
	for _, e := range []error{msfp.ErrUnreachable, msfp.ErrBackingOff, msfp.ErrServerBusy, msfp.ErrRateLimited,
		msfp.ErrPeerFailed, msfp.ErrCertificateRefused, msfp.ErrRefusedDestination} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

func (p *Panel) checkPeer(w http.ResponseWriter, r *http.Request, _ *session) {
	eng := p.fed(w)
	if eng == nil {
		return
	}
	ctx, id := r.Context(), r.PathValue("id")
	peer, err := eng.Check(ctx, id)
	failed := unreachable(err)
	switch {
	case errors.Is(err, msfp.ErrNotFound):
		notFound(w, "No such server")
		return
	case err == nil:
	case unreachable(err), errors.Is(err, msfp.ErrUnknown), errors.Is(err, msfp.ErrUncallable),
		errors.Is(err, msfp.ErrSuspended), errors.Is(err, msfp.ErrKeyLost):
		// It's shown as it stands now: gone (it doesn't know us), or
		// whatever made it not asked.
		if peer, err = eng.Peer(ctx, id); err != nil {
			p.fail(w, "reading a peer", err)
			return
		}
	default:
		p.fail(w, "checking a peer", err)
		return
	}
	counts, err := p.Store.PeerTitleCounts(ctx)
	if err != nil {
		p.fail(w, "counting peers' titles", err)
		return
	}
	j := map[string]any{"peer": peerCounted(peer, counts)}
	if failed {
		j["unreachable"] = true
	}
	writeJSON(w, http.StatusOK, j)
}

func (p *Panel) endPeer(w http.ResponseWriter, r *http.Request, s *session) {
	eng := p.fed(w)
	if eng == nil {
		return
	}
	switch err := eng.End(r.Context(), r.PathValue("id")); {
	case errors.Is(err, msfp.ErrNotFound):
		notFound(w, "No such server")
		return
	case err != nil:
		p.fail(w, "ending a pairing", err)
		return
	}
	p.Logger.Info("a pairing was ended in the control panel", "peer", r.PathValue("id"), "by", s.logID)
	w.WriteHeader(http.StatusNoContent)
}

// codeText gives a live invite's code again, to copy.
func (p *Panel) codeText(w http.ResponseWriter, r *http.Request, _ *session) {
	eng := p.fed(w)
	if eng == nil {
		return
	}
	text, err := eng.CodeText(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, msfp.ErrNotFound):
		notFound(w, "No such invite: it was used, withdrawn, or it stopped working")
		return
	case err != nil:
		p.fail(w, "reading an invite", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"code": text})
}

// emailCode sends a live invite by email, as many times as wanted.
func (p *Panel) emailCode(w http.ResponseWriter, r *http.Request, s *session) {
	eng := p.fed(w)
	if eng == nil {
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	email := strings.TrimSpace(req.Email)
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email || len(email) > 254 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "That isn't an email address."})
		return
	}
	if p.CodeMail == nil || !p.CodeMail.CanSendMail() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "Email is off: set it up first (Server, Emails)."})
		return
	}
	ctx, id := r.Context(), r.PathValue("id")
	text, err := eng.CodeText(ctx, id)
	if errors.Is(err, msfp.ErrNotFound) {
		notFound(w, "No such invite: it was used, withdrawn, or it stopped working")
		return
	} else if err != nil {
		p.fail(w, "reading an invite", err)
		return
	}
	var group string
	var expires time.Time
	if codes, err := eng.Codes(ctx); err == nil {
		for _, c := range codes {
			if c.ID == id {
				expires = c.Expires
				if c.Group != "" {
					if g, err := eng.Group(ctx, c.Group); err == nil {
						group = g.Name
					}
				}
			}
		}
	}
	if err := p.CodeMail.SendPairingCode(ctx, email, text, group, expires); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"type": "unreachable", "detail": err.Error()})
		return
	}
	p.Logger.Info("an invite was emailed from the control panel", "code", id, "by", s.logID)
	w.WriteHeader(http.StatusNoContent)
}

// fedChanges waits, up to 25 seconds, for federation to change after the
// number the page last saw, and says the number now: the page reloads its
// lists when it moved (a server waiting, a pairing, a request, a group
// changed, a catalog read).
func (p *Panel) fedChanges(w http.ResponseWriter, r *http.Request, _ *session) {
	if p.FedChanges == nil {
		writeJSON(w, http.StatusOK, map[string]uint64{"seq": 0})
		return
	}
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, map[string]uint64{"seq": p.FedChanges.Wait(ctx, after)})
}

// ourLibraries lists this server's libraries: what it may share, before a
// group exists (creating or joining one).
func (p *Panel) ourLibraries(w http.ResponseWriter, r *http.Request, _ *session) {
	eng := p.fed(w)
	if eng == nil {
		return
	}
	libs, err := eng.HostLibraries(r.Context())
	if err != nil {
		p.fail(w, "listing this server's libraries", err)
		return
	}
	type lib struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		// Kind: msfp-go's revised v0 sends no library kind; "" until plan
		// phase 7 types a library by its first titles.
		Kind string `json:"kind"`
	}
	out := []lib{}
	for _, l := range libs {
		out = append(out, lib{ID: l.ID, Name: l.Name})
	}
	writeJSON(w, http.StatusOK, out)
}

// pairDirectly asks a server known through a group to pair directly, or
// accepts its ask, or keeps one whose group was dissolved (MSFP core
// section 7.7).
func (p *Panel) pairDirectly(w http.ResponseWriter, r *http.Request, s *session) {
	p.relationAction(w, r, s, "pairing directly", func(eng *msfp.Engine, id string) error {
		_, err := eng.AskIndividual(r.Context(), id)
		return err
	})
}

// unpair ends the direct pairing: a server still in a shared group stays
// paired through it.
func (p *Panel) unpair(w http.ResponseWriter, r *http.Request, s *session) {
	p.relationAction(w, r, s, "unpairing", func(eng *msfp.Engine, id string) error {
		return eng.EndIndividual(r.Context(), id)
	})
}

// refuseDirect refuses a server's ask to pair directly, or lets go of one
// whose group was dissolved.
func (p *Panel) refuseDirect(w http.ResponseWriter, r *http.Request, s *session) {
	p.relationAction(w, r, s, "refusing to pair directly", func(eng *msfp.Engine, id string) error {
		return eng.RefuseIndividual(r.Context(), id)
	})
}

// blockPeer ends everything with a server and keeps it out of every group.
func (p *Panel) blockPeer(w http.ResponseWriter, r *http.Request, s *session) {
	p.relationAction(w, r, s, "blocking a server", func(eng *msfp.Engine, id string) error {
		return eng.BlockPeer(r.Context(), id)
	})
}

func (p *Panel) relationAction(w http.ResponseWriter, r *http.Request, s *session, doing string, fn func(eng *msfp.Engine, id string) error) {
	eng := p.fed(w)
	if eng == nil {
		return
	}
	id := r.PathValue("id")
	switch err := fn(eng, id); {
	case errors.Is(err, msfp.ErrNotFound):
		notFound(w, "No such server")
		return
	case errors.Is(err, msfp.ErrNotPaired):
		writeJSON(w, http.StatusConflict, map[string]string{"type": "not-paired", "detail": "That server isn't paired yet."})
		return
	case err != nil:
		p.fail(w, doing, err)
		return
	}
	p.Logger.Info("federation: "+doing+" in the control panel", "peer", id, "by", s.logID)
	w.WriteHeader(http.StatusNoContent)
}

// peerCuts lists the groups that ending this pairing would cut this
// server off from (MSFP groups section 6, "No way in"): the panel names
// them before it ends.
func (p *Panel) peerCuts(w http.ResponseWriter, r *http.Request, _ *session) {
	eng := p.fed(w)
	if eng == nil {
		return
	}
	cuts, err := eng.GroupsCutBy(r.Context(), r.PathValue("id"))
	if err != nil {
		p.fail(w, "listing the groups a pairing's end cuts off", err)
		return
	}
	out := make([]map[string]string, 0, len(cuts))
	for _, g := range cuts {
		out = append(out, map[string]string{"id": g.ID, "name": g.Name})
	}
	writeJSON(w, http.StatusOK, out)
}

// pairingProblem explains why a code couldn't be used.
func (p *Panel) pairingProblem(w http.ResponseWriter, err error) {
	detail := ""
	switch {
	case errors.Is(err, msfp.ErrCodeInvalid):
		detail = "That isn't a pairing code. Paste the whole code, starting with msfp-pair-0."
	case errors.Is(err, msfp.ErrPlainHTTP):
		detail = "That code is for a server at a plain http:// address. Servers pair over HTTPS only: ask its operator to give it an https address, then a new code."
	case errors.Is(err, msfp.ErrOwnCode):
		detail = "That's this server's own code: send it to the other server's operator instead."
	case errors.Is(err, msfp.ErrAlreadyPeer):
		detail = "That server is already in the list."
	case errors.Is(err, msfp.ErrAlreadyMember):
		detail = "This server is already in that group, banned from it, or waiting to join it (under Groups)."
	case errors.Is(err, msfp.ErrNotSupported):
		detail = "The server that made the code doesn't speak groups with this one (another version of the protocol, or it hasn't said yet). Try Check on it, then again."
	case errors.Is(err, msfp.ErrCodeExpired):
		detail = "That code stopped working: its time is up. Ask for a new one."
	case errors.Is(err, msfp.ErrCodeRefused):
		detail = "The other server refused the code: it was used up, withdrawn, or its time is up. Ask for a new one."
	case errors.Is(err, msfp.ErrRefusedDestination):
		detail = "That code is for an address this server never connects to (a local or private one). Ask its operator for a code with its public address."
	case errors.Is(err, msfp.ErrCertificateRefused):
		detail = "The other server's HTTPS certificate wasn't accepted: it must be valid for its address. Ask its operator to check it."
	case unreachable(err):
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
	eng := p.fed(w)
	if eng == nil {
		return
	}
	ctx := r.Context()
	peer, ok := p.pairedPeer(w, r, eng)
	if !ok {
		return
	}
	ours, err := eng.Offers(ctx, peer.ID)
	if err != nil {
		p.fail(w, "listing libraries", err)
		return
	}
	// Our names for the libraries we want, kept by Overfin.
	labels, err := p.Store.PeerWantLabels(ctx, peer.ID)
	if err != nil {
		p.fail(w, "reading wants", err)
		return
	}
	type lib struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		// Kind: msfp-go's revised v0 sends no library kind; "" until plan
		// phase 7 types a library by its first titles.
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
		outOurs = append(outOurs, lib{ID: l.ID, Name: l.Name, Picked: l.Offered})
	}
	theirs, err := eng.TheirLibraries(ctx, peer.ID)
	reachable := err == nil
	if reachable {
		// What it says we may use of it and what it calls itself, read
		// again with its libraries.
		if again, err := eng.Peer(ctx, peer.ID); err == nil {
			peer = again
		}
	} else {
		// Those we want only, as last read.
		wants, err := eng.Wants(ctx, peer.ID)
		if err != nil {
			p.fail(w, "reading wants", err)
			return
		}
		theirs = slices.DeleteFunc(wants, func(l msfp.WantedLibrary) bool { return !l.Wanted })
	}
	outTheirs := []lib{}
	for _, l := range theirs {
		outTheirs = append(outTheirs, lib{ID: l.ID, Name: l.Name, Picked: l.Wanted, Gone: l.Gone,
			Label: labels[l.ID], Shown: l.Name + " (" + peer.Name + ")"})
	}
	out := map[string]any{"ours": outOurs, "theirs": outTheirs, "reachable": reachable,
		"notSupported": errors.Is(err, msfp.ErrNotSupported), "uncallable": errors.Is(err, msfp.ErrUncallable),
		"limits": limitsOf(peer.Limits)}
	if peer.TheirLimits != nil {
		out["theirLimits"] = limitsOf(*peer.TheirLimits)
	}
	writeJSON(w, http.StatusOK, out)
}

func (p *Panel) setSharing(w http.ResponseWriter, r *http.Request, s *session) {
	eng := p.fed(w)
	if eng == nil {
		return
	}
	var req struct {
		Offer  []string    `json:"offer"`
		Want   []string    `json:"want"`
		Limits *limitsJSON `json:"limits"`
		// Labels are our names for wanted libraries' entries in the site's
		// menu, by the other server's library ID ("" for its own names).
		Labels map[string]string `json:"labels"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request"})
		return
	}
	ctx := r.Context()
	peer, ok := p.pairedPeer(w, r, eng)
	if !ok {
		return
	}
	if req.Limits != nil {
		l := msfp.Limits{Streams: req.Limits.Streams, Transcodes: req.Limits.Transcodes, BitrateKbps: req.Limits.BitrateKbps}
		err := eng.SetLimits(ctx, peer.ID, l)
		if errors.Is(err, msfp.ErrInvalidLimits) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request",
				"detail": "Streams and transcodes go from 0 to 50, with no more transcodes than streams; the bitrate cap from 0 (none) to 1,000,000 kbps."})
			return
		}
		if err != nil {
			p.fail(w, "setting a peer's limits", err)
			return
		}
	}
	if err := eng.SetOffers(ctx, peer.ID, req.Offer); err != nil {
		p.fail(w, "setting offers", err)
		return
	}
	// What we want changes only when it did: picking needs the other
	// server's list, so it must be reachable then.
	wants, err := eng.Wants(ctx, peer.ID)
	if err != nil {
		p.fail(w, "reading wants", err)
		return
	}
	now := []string{}
	for _, w := range wants {
		if w.Wanted {
			now = append(now, w.ID)
		}
	}
	slices.Sort(now)
	asked := slices.Clone(req.Want)
	slices.Sort(asked)
	asked = slices.Compact(asked)
	if !slices.Equal(now, asked) {
		// Its titles are read at once, for what we want now.
		if err := eng.SetWants(ctx, peer.ID, req.Want); err != nil {
			p.wantsProblem(w, err)
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

// wantsProblem explains why what we want of a peer couldn't be saved.
func (p *Panel) wantsProblem(w http.ResponseWriter, err error) {
	detail := ""
	switch {
	case unreachable(err), errors.Is(err, msfp.ErrUnknown):
		detail = "What you offer is saved, but what you want couldn't be: the other server can't be reached now. Try again later."
	case errors.Is(err, msfp.ErrUncallable):
		detail = "What you offer is saved, but what you want couldn't be: the other server can't be called, so it shares nothing with this one."
	case errors.Is(err, msfp.ErrSuspended):
		detail = "What you offer is saved, but what you want couldn't be: the other server revoked its key."
	case errors.Is(err, msfp.ErrNotSupported):
		detail = "What you offer is saved, but what you want couldn't be: the other server doesn't share libraries with this one (it speaks another version of the protocol, or hasn't said which yet). Try Check, then again."
	default:
		p.fail(w, "setting wants", err)
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": detail})
}

// pairedPeer is the paired server the request names; ok is false after
// answering.
func (p *Panel) pairedPeer(w http.ResponseWriter, r *http.Request, eng *msfp.Engine) (msfp.PeerView, bool) {
	peer, err := eng.Peer(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, msfp.ErrNotFound):
		notFound(w, "No such server")
		return peer, false
	case err != nil:
		p.fail(w, "reading a peer", err)
		return peer, false
	case peer.State != msfp.Paired:
		writeJSON(w, http.StatusConflict, map[string]string{"type": "not-paired", "detail": "Sharing starts once both operators have accepted."})
		return peer, false
	}
	return peer, true
}
