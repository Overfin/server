package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/msfp-io/msfp-go"
)

// Groups (MSFP groups part): servers whose operators know each other join
// one group with a code, then pair with each member that accepts, without
// more codes. The engine keeps the group's signed list and does every
// step; the panel shows its views and calls its actions.

type groupJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CapPerDay int64  `json:"capPerDay"`
	// Role is this server's: "owner", "admin" or "member"; Me its member
	// ID in the group.
	Role  string `json:"role"`
	Me    string `json:"me"`
	Owner string `json:"owner"`
	Heir  string `json:"heir,omitempty"`
	// Accept: this server automatically accepts new members, at once;
	// false: you confirm each.
	Accept bool `json:"accept"`
	// Offers counts the libraries offered to the group; Pool is what
	// this server lends the group's members, together.
	Offers    int        `json:"offers"`
	Pool      limitsJSON `json:"pool"`
	Dissolved bool       `json:"dissolved,omitempty"`
	Joining   bool       `json:"joining,omitempty"`
	// NoWayIn: this server has no pairing through the group, which has
	// other members: nothing reaches them from here, nor here from them.
	NoWayIn  bool              `json:"noWayIn,omitempty"`
	Members  []groupMemberJSON `json:"members"`
	Banned   []groupBanJSON    `json:"banned"`
	Requests []groupRequest    `json:"requests"`
	// Joins are servers already paired with this one that used its code
	// for the group, waiting for you to accept them in.
	Joins []groupJoinJSON `json:"joins"`
	// Links are the pairings made or kept through this group: Only when
	// nothing else holds them (no other group, no pairing by hand).
	Links []groupLinkJSON `json:"links"`
}

type groupMemberJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	Role        string `json:"role"`
	// InvitedBy is the inviter's name ("" for the founder).
	InvitedBy string `json:"invitedBy,omitempty"`
	JoinedAt  string `json:"joinedAt"`
	// Peer is the pairing with it, "" for none; Paired, whether it's
	// paired now. Refused: you refused its requests; Asked: your request
	// to it waits for its answer. Me: this server.
	Peer    string `json:"peer,omitempty"`
	Paired  bool   `json:"paired,omitempty"`
	Refused bool   `json:"refused,omitempty"`
	Asked   bool   `json:"asked,omitempty"`
	// Individual: also paired directly with it.
	Individual bool `json:"individual,omitempty"`
	Me         bool `json:"me,omitempty"`
}

type groupBanJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	Reason      string `json:"reason,omitempty"`
}

type groupRequest struct {
	Mail        string `json:"mail"`
	From        string `json:"from"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	ReceivedAt  string `json:"receivedAt"`
	// Waited: the member has been in the group the group's days.
	Waited bool `json:"waited"`
}

type groupJoinJSON struct {
	Peer        string `json:"peer"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	ReceivedAt  string `json:"receivedAt"`
}

type groupLinkJSON struct {
	Peer string `json:"peer"`
	Name string `json:"name"`
	Only bool   `json:"only"`
}

// groupOf is a group as the panel shows it; peers are this server's, for
// the pairings with its members.
func groupOf(v msfp.GroupView, peers []msfp.PeerView) groupJSON {
	byID := map[string]msfp.PeerView{}
	for _, p := range peers {
		byID[p.ID] = p
	}
	names := map[string]string{}
	for _, m := range v.Members {
		names[m.ID] = m.Name
	}
	j := groupJSON{ID: v.ID, Name: v.Name, CapPerDay: v.Settings.CapPerDay,
		Role: v.Role, Me: v.Me, Owner: v.Owner, Heir: v.Heir, Accept: v.Accept, Offers: len(v.Offers),
		Pool: limitsOf(v.Pool), Dissolved: v.Dissolved, Joining: v.Joining, NoWayIn: v.NoWayIn,
		Members: []groupMemberJSON{}, Banned: []groupBanJSON{}, Requests: []groupRequest{}, Joins: []groupJoinJSON{}, Links: []groupLinkJSON{}}
	for _, m := range v.Members {
		mj := groupMemberJSON{ID: m.ID, Name: m.Name, Fingerprint: m.Fingerprint, Role: m.Role,
			InvitedBy: names[m.InvitedBy], JoinedAt: stamp(m.Joined), Peer: m.Peer, Refused: m.Refused, Asked: m.Asked,
			Me: m.ID == v.Me}
		if p, ok := byID[m.Peer]; ok {
			mj.Paired = p.State == msfp.Paired
			mj.Individual = p.Individual
		}
		j.Members = append(j.Members, mj)
	}
	for _, b := range v.Banned {
		j.Banned = append(j.Banned, groupBanJSON{ID: b.ID, Name: b.Name, Fingerprint: b.Fingerprint, Reason: b.Reason})
	}
	for _, r := range v.Requests {
		j.Requests = append(j.Requests, groupRequest{Mail: r.Mail, From: r.From, Name: r.Name,
			Fingerprint: r.Fingerprint, ReceivedAt: stamp(r.Received), Waited: r.Waited})
	}
	for _, jn := range v.Joins {
		j.Joins = append(j.Joins, groupJoinJSON{Peer: jn.Peer, Name: jn.Name, Fingerprint: jn.Fingerprint, ReceivedAt: stamp(jn.Received)})
	}
	for _, p := range peers {
		if slices.Contains(p.Groups, v.ID) {
			j.Links = append(j.Links, groupLinkJSON{Peer: p.ID, Name: p.Name, Only: !p.Individual && !p.IndividualAsked && len(p.Groups) == 1})
		}
	}
	return j
}

// groups is every group this server is in, for the federation view.
func (p *Panel) groups(r *http.Request, eng *msfp.Engine, peers []msfp.PeerView) ([]groupJSON, error) {
	list, err := eng.Groups(r.Context())
	if errors.Is(err, msfp.ErrNoGroups) {
		return []groupJSON{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []groupJSON{}
	for _, g := range list {
		out = append(out, groupOf(g, peers))
	}
	return out, nil
}

// groupProblem answers an action on a group that failed.
func (p *Panel) groupProblem(w http.ResponseWriter, doing string, err error) {
	switch {
	case errors.Is(err, msfp.ErrNotFound):
		notFound(w, "No such group, member or request")
	case errors.Is(err, msfp.ErrNotMember):
		writeJSON(w, http.StatusConflict, map[string]string{"type": "not-member",
			"detail": "That server isn't in the group (any more), or this one isn't."})
	case errors.Is(err, msfp.ErrNoRight):
		writeJSON(w, http.StatusForbidden, map[string]string{"type": "no-right",
			"detail": "Your role in this group doesn't allow that. The owner names admins, gives the group away and lifts bans; admins and the owner invite and ban (admins ban members only)."})
	case errors.Is(err, msfp.ErrCodeOptions):
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request",
			"detail": "A group needs a name (80 characters at most); members wait 0 to 30 days; each inviter brings in 1 to 100 servers a day."})
	case errors.Is(err, msfp.ErrInvalidLimits):
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request",
			"detail": "Streams and transcodes go from 0 to 50, with no more transcodes than streams; the bitrate cap from 0 (none) to 1,000,000 kbps."})
	case errors.Is(err, msfp.ErrNoWayIn):
		writeJSON(w, http.StatusConflict, map[string]string{"type": "no-way-in",
			"detail": "This server has no pairing through this group any more: nothing it sends reaches the other members, and nothing from them reaches it."})
	case errors.Is(err, msfp.ErrNoGroups):
		writeJSON(w, http.StatusConflict, map[string]string{"type": "unavailable", "detail": "Groups aren't available on this server."})
	case unreachable(err):
		writeJSON(w, http.StatusBadGateway, map[string]string{"type": "unreachable",
			"detail": "The other server couldn't be reached now. Try again later."})
	default:
		p.fail(w, doing, err)
	}
}

// decodeBody reads a request's JSON body into v; false after answering.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request"})
		return false
	}
	return true
}

// groupSettingsReq is a group's name and settings, as the panel sends
// them.
type groupSettingsReq struct {
	Name      string `json:"name"`
	CapPerDay int64  `json:"capPerDay"`
	// On creation only: what this server shares with the group, and the
	// limits it lends it.
	Offer  []string    `json:"offer"`
	Limits *limitsJSON `json:"limits"`
}

func (p *Panel) createGroup(w http.ResponseWriter, r *http.Request, s *session) {
	eng := p.fed(w)
	if eng == nil {
		return
	}
	var req groupSettingsReq
	if !decodeBody(w, r, &req) {
		return
	}
	ctx := r.Context()
	g, err := eng.CreateGroup(ctx, req.Name, msfp.GroupSettings{CapPerDay: req.CapPerDay})
	if err != nil {
		p.groupProblem(w, "creating a group", err)
		return
	}
	if req.Limits != nil {
		l := msfp.Limits{Streams: req.Limits.Streams, Transcodes: req.Limits.Transcodes, BitrateKbps: req.Limits.BitrateKbps}
		if err := eng.SetGroupPool(ctx, g.ID, l); err != nil {
			p.groupProblem(w, "setting a new group's limits", err)
			return
		}
	}
	if len(req.Offer) > 0 {
		if err := eng.SetGroupOffers(ctx, g.ID, req.Offer); err != nil {
			p.groupProblem(w, "setting what a new group is offered", err)
			return
		}
	}
	if v, err := eng.Group(ctx, g.ID); err == nil {
		g = v
	}
	p.Logger.Info("a group was created in the control panel", "group", g.ID, "by", s.logID)
	writeJSON(w, http.StatusCreated, groupOf(g, nil))
}

// groupAction runs one action on a group, logs it and answers 204.
func (p *Panel) groupAction(w http.ResponseWriter, r *http.Request, s *session, doing string, fn func(eng *msfp.Engine, id string) error) {
	eng := p.fed(w)
	if eng == nil {
		return
	}
	id := r.PathValue("id")
	if err := fn(eng, id); err != nil {
		p.groupProblem(w, doing, err)
		return
	}
	p.Logger.Info("groups: "+doing+" in the control panel", "group", id, "by", s.logID)
	w.WriteHeader(http.StatusNoContent)
}

func (p *Panel) setGroupSettings(w http.ResponseWriter, r *http.Request, s *session) {
	var req groupSettingsReq
	if !decodeBody(w, r, &req) {
		return
	}
	p.groupAction(w, r, s, "changing a group's settings", func(eng *msfp.Engine, id string) error {
		return eng.SetGroupSettings(r.Context(), id, req.Name, msfp.GroupSettings{CapPerDay: req.CapPerDay})
	})
}

func (p *Panel) setGroupAnswer(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		Accept bool `json:"accept"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	p.groupAction(w, r, s, "changing how requests are answered", func(eng *msfp.Engine, id string) error {
		return eng.SetGroupAnswer(r.Context(), id, req.Accept)
	})
}

// groupSharing is what this server offers a group, library by library,
// and the pool it lends it.
func (p *Panel) groupSharing(w http.ResponseWriter, r *http.Request, _ *session) {
	eng := p.fed(w)
	if eng == nil {
		return
	}
	ctx, id := r.Context(), r.PathValue("id")
	g, err := eng.Group(ctx, id)
	if err != nil {
		p.groupProblem(w, "reading a group", err)
		return
	}
	libs, err := eng.GroupOffers(ctx, id)
	if err != nil {
		p.groupProblem(w, "listing libraries", err)
		return
	}
	type lib struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Kind   string `json:"kind"`
		Picked bool   `json:"picked"`
	}
	ours := []lib{}
	for _, l := range libs {
		ours = append(ours, lib{ID: l.ID, Name: l.Name, Kind: l.Kind, Picked: l.Offered})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ours": ours, "limits": limitsOf(g.Pool)})
}

// groupShows is what this server shows from a group: each member's
// libraries offered to it, and whether each is shown (all by default).
func (p *Panel) groupShows(w http.ResponseWriter, r *http.Request, _ *session) {
	eng := p.fed(w)
	if eng == nil {
		return
	}
	v, err := eng.GroupShows(r.Context(), r.PathValue("id"))
	if err != nil {
		p.groupProblem(w, "reading what a group shows", err)
		return
	}
	type lib struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Kind  string `json:"kind"`
		Shown bool   `json:"shown"`
	}
	type member struct {
		Member    string `json:"member"`
		Name      string `json:"name"`
		Libraries []lib  `json:"libraries"`
	}
	out := []member{}
	for _, m := range v.Members {
		mm := member{Member: m.Member, Name: m.Name, Libraries: []lib{}}
		for _, l := range m.Libraries {
			mm.Libraries = append(mm.Libraries, lib{ID: l.ID, Name: l.Name, Kind: l.Kind, Shown: l.Shown})
		}
		out = append(out, mm)
	}
	writeJSON(w, http.StatusOK, out)
}

func (p *Panel) setGroupShows(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		Hidden []struct {
			Member  string `json:"member"`
			Library string `json:"library"`
		} `json:"hidden"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	p.groupAction(w, r, s, "changing what a group shows", func(eng *msfp.Engine, id string) error {
		hidden := make([]msfp.HiddenLibrary, 0, len(req.Hidden))
		for _, h := range req.Hidden {
			hidden = append(hidden, msfp.HiddenLibrary{Member: h.Member, Library: h.Library})
		}
		return eng.SetGroupShows(r.Context(), id, hidden)
	})
}

func (p *Panel) setGroupSharing(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		Offer  []string   `json:"offer"`
		Limits limitsJSON `json:"limits"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	p.groupAction(w, r, s, "changing what a group is offered", func(eng *msfp.Engine, id string) error {
		ctx := r.Context()
		l := msfp.Limits{Streams: req.Limits.Streams, Transcodes: req.Limits.Transcodes, BitrateKbps: req.Limits.BitrateKbps}
		if err := eng.SetGroupPool(ctx, id, l); err != nil {
			return err
		}
		return eng.SetGroupOffers(ctx, id, req.Offer)
	})
}

func (p *Panel) setGroupAdmin(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		On bool `json:"on"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	p.groupAction(w, r, s, "changing an admin", func(eng *msfp.Engine, id string) error {
		return eng.SetAdmin(r.Context(), id, r.PathValue("member"), req.On)
	})
}

// memberReq names a member ("" for none, where that means something).
type memberReq struct {
	Member string `json:"member"`
}

func (p *Panel) setGroupHeir(w http.ResponseWriter, r *http.Request, s *session) {
	var req memberReq
	if !decodeBody(w, r, &req) {
		return
	}
	p.groupAction(w, r, s, "naming the heir", func(eng *msfp.Engine, id string) error {
		return eng.SetHeir(r.Context(), id, req.Member)
	})
}

func (p *Panel) giveGroup(w http.ResponseWriter, r *http.Request, s *session) {
	var req memberReq
	if !decodeBody(w, r, &req) {
		return
	}
	p.groupAction(w, r, s, "giving a group away", func(eng *msfp.Engine, id string) error {
		return eng.GiveGroup(r.Context(), id, req.Member)
	})
}

func (p *Panel) banGroupMember(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		Member string `json:"member"`
		Reason string `json:"reason"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	p.groupAction(w, r, s, "banning a member", func(eng *msfp.Engine, id string) error {
		return eng.Ban(r.Context(), id, req.Member, req.Reason)
	})
}

func (p *Panel) unbanGroupMember(w http.ResponseWriter, r *http.Request, s *session) {
	p.groupAction(w, r, s, "lifting a ban", func(eng *msfp.Engine, id string) error {
		return eng.Unban(r.Context(), id, r.PathValue("member"))
	})
}

func (p *Panel) blockGroupMember(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		Member   string `json:"member"`
		Invitees bool   `json:"invitees"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	p.groupAction(w, r, s, "blocking a member", func(eng *msfp.Engine, id string) error {
		return eng.Block(r.Context(), id, req.Member, req.Invitees)
	})
}

func (p *Panel) acceptGroupRequest(w http.ResponseWriter, r *http.Request, s *session) {
	p.groupAction(w, r, s, "accepting a request", func(eng *msfp.Engine, id string) error {
		return eng.AcceptRequest(r.Context(), id, r.PathValue("mail"))
	})
}

func (p *Panel) acceptGroupJoin(w http.ResponseWriter, r *http.Request, s *session) {
	p.groupAction(w, r, s, "accepting a server into a group", func(eng *msfp.Engine, id string) error {
		return eng.AcceptJoin(r.Context(), id, r.PathValue("peer"))
	})
}

func (p *Panel) refuseGroupJoin(w http.ResponseWriter, r *http.Request, s *session) {
	p.groupAction(w, r, s, "refusing a server into a group", func(eng *msfp.Engine, id string) error {
		return eng.RefuseJoin(r.Context(), id, r.PathValue("peer"))
	})
}

func (p *Panel) refuseGroupRequest(w http.ResponseWriter, r *http.Request, s *session) {
	p.groupAction(w, r, s, "refusing a request", func(eng *msfp.Engine, id string) error {
		return eng.RefuseRequest(r.Context(), id, r.PathValue("mail"))
	})
}

func (p *Panel) liftGroupRefusal(w http.ResponseWriter, r *http.Request, s *session) {
	p.groupAction(w, r, s, "lifting a refusal", func(eng *msfp.Engine, id string) error {
		return eng.LiftRefusal(r.Context(), id, r.PathValue("member"))
	})
}

func (p *Panel) askGroupMember(w http.ResponseWriter, r *http.Request, s *session) {
	p.groupAction(w, r, s, "asking a member", func(eng *msfp.Engine, id string) error {
		return eng.RequestMember(r.Context(), id, r.PathValue("member"))
	})
}

func (p *Panel) leaveGroup(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		// Ask lists the servers only this group pairs us with, asked to
		// stay paired directly first.
		Ask []string `json:"ask"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	p.groupAction(w, r, s, "leaving a group", func(eng *msfp.Engine, id string) error {
		return eng.LeaveGroup(r.Context(), id, req.Ask)
	})
}

func (p *Panel) dissolveGroup(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		KeepPairings bool `json:"keepPairings"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	p.groupAction(w, r, s, "dissolving a group", func(eng *msfp.Engine, id string) error {
		return eng.DissolveGroup(r.Context(), id, req.KeepPairings)
	})
}
