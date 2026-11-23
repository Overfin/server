package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/msfp-io/msfp-go/wire"
	"github.com/overfin/server/internal/delivery"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
)

// This server's players on MSFP's media model (Overfin's plan, section
// 4.1): a player chooses a copy of the title's page, its audio and its
// delivery itself, and asks for exactly that with MSFP's play request,
// beside Overfin's own members (the title, the device, why it chose that
// delivery). Overfin plays it through the executor (deliveries.go) or
// refuses it as MSFP does; the answer is MSFP's playback object, with no
// URL: the player builds them from the ticket, under
// /api/v1/stream/{ticket}/.

// maxPlayBody bounds a play request's body.
const maxPlayBody = 16 << 10

// olderPlay are the members only the older body had (capabilities; the
// server chose): a body with one of them comes from a page older than
// this API, made to load again (start).
var olderPlay = []string{"capabilities", "itemId", "audioLanguage", "subtitleTrack", "queue"}

// playMembers are the members of the play request this server's players
// send: MSFP's (Overfin's titleId standing for its title) and Overfin's
// own. Any other is refused.
var playMembers = map[string]bool{"titleId": true, "versionId": true, "audioTrack": true, "delivery": true,
	"mediaType": true, "transcode": true, "audioRenditions": true, "startPositionMs": true, "maxBitrateKbps": true,
	"device": true, "reason": true}

// playJSON is the answer to a play: MSFP's playback object as it is, and
// Overfin's wrapper: what plays, and the episode after it.
type playJSON struct {
	wire.PlayAnswer
	Title *library.TitleSummary `json:"title,omitempty"`
	Next  *library.TitleSummary `json:"next,omitempty"`
}

// start starts a playback from MSFP's play request.
func (pb Playback) start(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPlayBody+1))
	if err != nil || len(body) > maxPlayBody {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(body, &members); err != nil {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	for _, m := range olderPlay {
		if _, ok := members[m]; ok {
			// A page still on an older player (a tab left open over an
			// update): "not signed in" sends it through sign-in, straight
			// back when the session is there, which loads the site again,
			// on the player of this API. Nothing of the older body is read.
			problem.Send(w, problem.NotAuthenticated, "")
			return
		}
	}
	pb.play(w, r, members)
}

// playAsk is a play request as read: MSFP's (its title Overfin's titleId),
// and Overfin's members.
type playAsk struct {
	req     wire.PlayRequest
	titleID string
	device  string
	reason  string
}

// readPlay reads a play request's members: Overfin's, then MSFP's as
// MSFP reads them (wire.ReadPlay: nulls, tokens, values, bounds), the
// track IDs' forms included. false: malformed (400).
func readPlay(members map[string]json.RawMessage) (playAsk, bool) {
	var a playAsk
	msfpMembers := map[string]json.RawMessage{}
	for name, raw := range members {
		if !playMembers[name] {
			return a, false
		}
		var dst *string
		switch name {
		case "titleId":
			dst = &a.titleID
		case "device":
			dst = &a.device
		case "reason":
			dst = &a.reason
		default:
			msfpMembers[name] = raw
			continue
		}
		var v *string
		if err := json.Unmarshal(raw, &v); err != nil || v == nil {
			return a, false
		}
		*dst = *v
	}
	if a.titleID == "" || (a.device != "" && !deviceRe.MatchString(a.device)) || (a.reason != "" && !playback.KnownReason(a.reason)) {
		return a, false
	}
	// MSFP's title stands for Overfin's titleId, checked apart.
	msfpMembers["title"] = json.RawMessage(`"movie/tmdb/1"`)
	body, err := json.Marshal(msfpMembers)
	if err != nil {
		return a, false
	}
	req, err := wire.ReadPlay(body, func(string) bool { return true })
	if err != nil || !req.TrackIDsOK() {
		return a, false
	}
	req.Title = ""
	a.req = req
	return a, true
}

// play starts a playback from MSFP's play request.
func (pb Playback) play(w http.ResponseWriter, r *http.Request, members map[string]json.RawMessage) {
	ask, ok := readPlay(members)
	if !ok {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	ref, ok := titleRef(ask.titleID)
	if !ok {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	l := pb.Library
	caller := l.Sessions.Current(w, r)
	if caller == nil {
		return
	}
	p := caller.Profile
	ctx := r.Context()
	if !playable(ref) {
		problem.Send(w, problem.NotFound, "")
		return
	}
	t, ok := l.visibleTitleRef(w, ctx, p, ref)
	if !ok {
		return
	}
	copies, err := l.titleCopies(ctx, p, t)
	if err != nil {
		l.storeError(w, err)
		return
	}
	// The copy asked for, else the first, the server's preferred (MSFP
	// playback section 2.6); one the title doesn't have (any more): not
	// found, for the player to read the title again.
	i := slices.IndexFunc(copies, func(c store.IndexedCopy) bool { return c.CopyID == ask.req.VersionID })
	if len(copies) == 0 || ask.req.VersionID != "" && i < 0 {
		// A paired server's copy, or none.
		pb.playPeerCopy(w, r, caller, t, ask)
		return
	}
	if i < 0 {
		i = 0
	}
	c := copies[i]
	ja, _, ok := l.account(w, r)
	if !ok {
		return
	}
	titles, err := l.titlesOfItems(ctx, p, []string{c.ItemID})
	if err != nil {
		l.storeError(w, err)
		return
	}
	lim, err := pb.limitsOf(ctx, caller.Account.Template, p.AccountID)
	if err != nil {
		pb.Logger.Error("listing the account's profiles failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	device := ask.device
	if device == "" {
		device = deviceName(r.UserAgent())
	}
	kbps := ask.req.BitrateCap(pb.MaxBitrateKbps, 0)
	a := deliveryArgs{lim: lim, device: device, accountID: p.AccountID, profileID: p.ID, sessionID: caller.Session.ID,
		auth: ja, reason: ask.reason}
	// Seek-bar previews, Overfin's own (a bonus), under the playback's
	// ticket; the final credits, for when it's watched.
	a.trickplay, a.previews = pb.ownPreviews(ctx, c)
	a.creditsMs = pb.credits(ctx, c)
	s, ok := pb.openDelivery(w, r, c, delivery.Request{Play: ask.req, CapKbps: kbps, Own: true}, a)
	if !ok {
		return
	}
	d := s.Delivery
	out := playJSON{PlayAnswer: wire.PlayAnswer{ID: s.ID, Ticket: s.Ticket, Protocol: d.Protocol, Method: d.Method(),
		Etag: c.ETag, PositionMs: d.Plan.StartMs, VersionID: c.CopyID, AudioTrack: d.Plan.Audio,
		Subtitles: servedSubtitles(s, c)}}
	if kbps > 0 {
		out.MaxBitrateKbps = &kbps
	}
	if len(titles) == 1 {
		out.Title = &titles[0]
		if ref, ok := titleRef(titles[0].ID); ok && ref.Kind == "episode" {
			next, err := l.nextEpisode(ctx, p, ref)
			if err != nil {
				pb.Logger.Warn("finding the next episode failed", "err", err) // a bonus: playback goes on
			}
			out.Next = next
		}
	}
	s.View = out
	pb.Tickets.Describe(s, describe(out.Title))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeOK(w, out)
}

// playPeerCopy plays a paired server's copy of t: the one the request
// names, else the first (when this server has none). Not found when the
// title has no such copy (any more): the player reads it again.
func (pb Playback) playPeerCopy(w http.ResponseWriter, r *http.Request, caller *Caller, t *store.TitleInfo, ask playAsk) {
	l, ctx, p := pb.Library, r.Context(), caller.Profile
	theirs, err := l.peerCopies(ctx, p, t)
	if err != nil {
		l.storeError(w, err)
		return
	}
	i := slices.IndexFunc(theirs, func(pc peerCopy) bool { return pc.ID == ask.req.VersionID })
	if ask.req.VersionID == "" && len(theirs) > 0 {
		i = 0
	}
	if i < 0 {
		problem.Send(w, problem.NotFound, "")
		return
	}
	pc := theirs[i]
	s, got, ok := pb.playRemote(w, r, caller, remotePlay{copy: pc, req: ask.req, key: t.Ref.Key(), device: ask.device, reason: ask.reason})
	if !ok {
		return
	}
	out := playJSON{PlayAnswer: wire.PlayAnswer{ID: s.ID, Ticket: s.Ticket, Protocol: got.Protocol, Method: got.Method,
		Etag: got.Etag, PositionMs: got.PositionMs, VersionID: pc.ID, AudioTrack: got.AudioTrack,
		AudioRenditions: got.AudioRenditions, Subtitles: got.Subtitles, ReadTitleAgain: got.CopyChanged}}
	if got.MaxBitrateKbps > 0 {
		kbps := got.MaxBitrateKbps
		out.MaxBitrateKbps = &kbps
	}
	if titles, err := l.titleSummaries(ctx, p, []string{t.Ref.Key()}); err == nil && len(titles) == 1 {
		out.Title = &titles[0]
		if t.Ref.Kind == "episode" {
			if next, err := l.nextEpisode(ctx, p, t.Ref); err == nil {
				out.Next = next
			}
		}
	}
	s.View = out
	pb.Tickets.Describe(s, describe(out.Title))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeOK(w, out)
}

// limitsOf are the limits a playback of an account starts under: its
// template's streams (one reserved for each profile, when it says so),
// the operator's transcodes.
func (pb Playback) limitsOf(ctx context.Context, tmpl store.Template, accountID string) (playback.Limits, error) {
	lim := playback.Limits{MaxStreams: tmpl.MaxStreams, RefuseOverLimit: pb.RefuseOverLimit, MaxTranscodes: pb.MaxTranscodes}
	if lim.MaxStreams > 0 && tmpl.ReserveStreams {
		profiles, err := pb.Library.Provisioner.Store.Profiles(ctx, accountID)
		if err != nil {
			return lim, err
		}
		lim.Reserve, lim.Profiles = true, len(profiles)
	}
	return lim, nil
}

// servedSubtitles are the copy's subtitle tracks served apart (MSFP video
// section 5.3): text as WebVTT, PGS as pictures; ASS as its text only,
// for now (no styled rendering, no fonts); DVD and DVB pictures not
// apart (rule 17). A track Jellyfin's numbering doesn't map isn't served.
func servedSubtitles(s *playback.Stream, c store.IndexedCopy) []wire.ServedSubtitle {
	var out []wire.ServedSubtitle
	for _, t := range c.Facts.SubtitleTracks {
		n, ok := wire.TrackID(t.ID, 's')
		if !ok {
			continue
		}
		if _, mapped := s.JellyfinSubtitle(n); !mapped {
			continue
		}
		switch {
		case s.TextSubtitle(n):
			out = append(out, wire.ServedSubtitle{ID: t.ID, Form: wire.FormText})
		case s.PictureSubtitle(n):
			out = append(out, wire.ServedSubtitle{ID: t.ID, Form: wire.FormPicture})
		}
		if len(out) == wire.MaxServedSubtitles {
			break
		}
	}
	return out
}

// ownPreviews are a copy's seek-bar previews when Overfin made some for
// its file (never Jellyfin's): their layout and the fingerprint they're
// kept by; nil and "" otherwise.
func (pb Playback) ownPreviews(ctx context.Context, c store.IndexedCopy) (*jellyfin.Trickplay, string) {
	if c.PreviewLayout == nil {
		return nil, ""
	}
	fp, own, ok := pb.Library.Previews.ForVersion(ctx, c.ItemID, c.SourceID)
	if !ok {
		return nil, ""
	}
	tp := &jellyfin.Trickplay{Width: own.Width, Height: own.Height, Columns: own.Columns, Rows: own.Rows,
		Count: own.Count, IntervalMs: own.IntervalMs}
	if sheetCount(*tp) == 0 {
		return nil, ""
	}
	return tp, fp
}

// credits is where a copy's final credits start: its last outro, when it
// starts in the second half; 0 otherwise (a bonus: 0 when reading fails).
func (pb Playback) credits(ctx context.Context, c store.IndexedCopy) int64 {
	segs, err := pb.Library.Provisioner.Store.CopySegments(ctx, c.ItemID, c.SourceID)
	if err != nil {
		pb.Logger.Warn("reading a copy's skip markers failed", "err", err)
		return 0
	}
	var at int64
	for _, sg := range segs {
		if sg.Kind == "outro" && sg.StartMs > at {
			at = sg.StartMs
		}
	}
	if d := c.Facts.DurationMs; at*2 < d || at >= d {
		return 0
	}
	return at
}

// startPreviews gives a copy's seek-bar previews before it plays (MSFP
// video section 5.6): a ticket its sheets are fetched under
// (/api/v1/stream/{ticket}/trickplay/{sheet}), for ten minutes, in this
// session and for this profile only, and the copy's etag. Not found when
// the profile may not see the copy, or it has no previews Overfin made.
func (pb Playback) startPreviews(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TitleID   string `json:"titleId"`
		VersionID string `json:"versionId"`
	}
	if !decode(r, &req) || !wire.ID(req.VersionID) {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	ref, ok := titleRef(req.TitleID)
	if !ok {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	l := pb.Library
	caller := l.Sessions.Current(w, r)
	if caller == nil {
		return
	}
	p := caller.Profile
	ctx := r.Context()
	if !playable(ref) {
		problem.Send(w, problem.NotFound, "")
		return
	}
	t, ok := l.visibleTitleRef(w, ctx, p, ref)
	if !ok {
		return
	}
	copies, err := l.titleCopies(ctx, p, t)
	if err != nil {
		l.storeError(w, err)
		return
	}
	i := slices.IndexFunc(copies, func(c store.IndexedCopy) bool { return c.CopyID == req.VersionID })
	if i < 0 {
		pb.startPeerPreviews(w, r, caller, t, req.VersionID)
		return
	}
	c := copies[i]
	tp, fp := pb.ownPreviews(ctx, c)
	if tp == nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	ticket, err := pb.Tickets.IssuePreviews(playback.PreviewTicket{SessionID: caller.Session.ID, ProfileID: p.ID,
		Previews: fp, Trickplay: *tp}, time.Now())
	if errors.Is(err, playback.ErrTooManyPreviews) {
		problem.Send(w, problem.RateLimited, "")
		return
	}
	writeOK(w, wire.PreviewsAnswer{Ticket: ticket, ETag: c.ETag})
}

// startPeerPreviews gives a paired server's copy's seek-bar previews
// before it plays, as startPreviews does this server's: a ticket of this
// server's, its sheets fetched from the paired server when asked
// (Engine.PreviewSheet: never ahead, never for a whole library), kept by
// the copy's etag and layout. Not found for a copy without previews.
func (pb Playback) startPeerPreviews(w http.ResponseWriter, r *http.Request, caller *Caller, t *store.TitleInfo, versionID string) {
	theirs, err := pb.Library.peerCopies(r.Context(), caller.Profile, t)
	if err != nil {
		pb.Library.storeError(w, err)
		return
	}
	i := slices.IndexFunc(theirs, func(pc peerCopy) bool { return pc.ID == versionID })
	if i < 0 || theirs[i].Copy.PreviewLayout == nil || pb.engine() == nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	pc := theirs[i]
	l := pc.Copy.PreviewLayout
	tp := jellyfin.Trickplay{Width: l.TileWidth, Height: l.TileHeight, Columns: l.Columns, Rows: l.Rows,
		Count: l.TileCount, IntervalMs: l.IntervalMs}
	if sheetCount(tp) == 0 {
		problem.Send(w, problem.NotFound, "")
		return
	}
	ticket, err := pb.Tickets.IssuePreviews(playback.PreviewTicket{SessionID: caller.Session.ID, ProfileID: caller.Profile.ID,
		Trickplay: tp, Peer: pc.Source.Peer, Key: pc.Source.Key, Version: pc.Copy.ID, Etag: pc.Copy.ETag}, time.Now())
	if errors.Is(err, playback.ErrTooManyPreviews) {
		problem.Send(w, problem.RateLimited, "")
		return
	}
	writeOK(w, wire.PreviewsAnswer{Ticket: ticket, ETag: pc.Copy.ETag})
}

// servePeerSheet serves a sheet of a paired server's copy's previews
// under a previews ticket: fetched from it by the engine (checked and
// re-encoded there), kept by the copy's etag and layout.
func (pb Playback) servePeerSheet(w http.ResponseWriter, r *http.Request, pt playback.PreviewTicket, sheet string) {
	n, ok := sheetNumber(&pt.Trickplay, sheet)
	eng := pb.engine()
	if !ok || eng == nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	tp := pt.Trickplay
	key := fmt.Sprintf("peer-previews/%s/%s/%dx%d-%dx%d-%d-%d/%d", pt.Peer, pt.Etag, tp.Width, tp.Height, tp.Columns, tp.Rows,
		tp.IntervalMs, tp.Count, n)
	data, err := pb.Library.Images.Fetched(r.Context(), key, func(ctx context.Context) ([]byte, error) {
		return eng.PreviewSheet(ctx, pt.Peer, pt.Key, pt.Version, n)
	})
	if err != nil {
		if r.Context().Err() == nil {
			pb.Logger.Info("a paired server's preview sheet failed", "peer", pt.Peer, "err", err)
		}
		problem.Send(w, problem.NotFound, "")
		return
	}
	writeSheet(w, data)
}
