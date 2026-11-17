package api

import (
	"context"
	"errors"
	"github.com/overfin/server/internal/textclean"
	"github.com/overfin/server/internal/tracks"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/overfin/server/internal/federation"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/lang"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
)

// A paired server's viewer plays one of our titles (ROADMAP.md milestone
// 6): their server asks us, signed, for the title it wants; we check the
// pairing, that the title is in a library we offer it, and the pairing's
// limits, pick our copy (one with the viewer's language when we have
// one), and answer with a playback whose stream their server fetches
// from here, with its ticket only, for the viewer, whose browser never
// asks here (rule 97). Everything personal (what they watch, where they
// are, their address) stays on their server.

// peerPlayRequest is MSFP's play (version 0, section 9.5). Its optional
// members are left out when empty, never sent as "" or null.
type peerPlayRequest struct {
	// Title is the title key: a film's or an episode's.
	Title           string                `json:"title"`
	Capabilities    playback.Capabilities `json:"capabilities"`
	StartPositionMs int64                 `json:"startPositionMs,omitempty"`
	MaxBitrateKbps  int                   `json:"maxBitrateKbps,omitempty"`
	// AudioLanguage and SubtitleLanguage are the viewer's, as tags: the
	// copy and the tracks are picked by them.
	AudioLanguage    string `json:"audioLanguage,omitempty"`
	SubtitleLanguage string `json:"subtitleLanguage,omitempty"`
	AudioTrack       string `json:"audioTrack,omitempty"`
	SubtitleTrack    string `json:"subtitleTrack,omitempty"`
}

var languageTagRe = regexp.MustCompile(`^[A-Za-z0-9-]{0,12}$`)

func (pb Playback) fed() *federation.Service {
	if pb.Federation == nil {
		return nil
	}
	return pb.Federation()
}

// peerPlay starts a paired server's viewer's playback.
func (pb Playback) peerPlay(w http.ResponseWriter, r *http.Request) {
	fed := pb.fed()
	if fed == nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	peer, body, nonce, ok := fed.PeerRequest(w, r)
	if !ok {
		return
	}
	var req peerPlayRequest
	if federation.DecodeJSON(body, &req) != nil {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	ref, known := store.ParseTitleKey(req.Title)
	// The same bounds as this server's own playbacks (a number past them
	// would wrap when made into Jellyfin's ticks or bits).
	if !known || (ref.Kind != "movie" && ref.Kind != "episode") || req.StartPositionMs < 0 || req.StartPositionMs > maxPositionMs ||
		req.MaxBitrateKbps < 0 || req.MaxBitrateKbps > maxKbps ||
		req.Capabilities.MaxBitrateKbps < 0 || req.Capabilities.MaxBitrateKbps > maxKbps ||
		!languageTagRe.MatchString(req.AudioLanguage) || !languageTagRe.MatchString(req.SubtitleLanguage) {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	// Nothing at all for this pairing: refused before anything else.
	if peer.Limits.Streams == 0 {
		problem.Send(w, problem.StreamLimitReached, "")
		return
	}
	l, ctx := pb.Library, r.Context()
	copies, err := l.Provisioner.Store.PeerCopies(ctx, peer.ID, req.Title)
	if err != nil {
		l.storeError(w, err)
		return
	}
	if len(copies) == 0 {
		problem.Send(w, problem.NotFound, "")
		return
	}
	item := pickCopy(copies, req.AudioLanguage)

	var pr jellyfin.PlaybackRequest
	pr.StartTimeTicks = req.StartPositionMs * 10_000
	// A malformed track is refused; a well-formed one is Jellyfin's to
	// hear only once it is one of the copy's (open, peerTracks).
	if _, ok := trackIndex(req.AudioTrack, 'a'); req.AudioTrack != "" && !ok {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	off := -1
	pr.SubtitleStreamIndex = &off
	if req.SubtitleTrack != "" && req.SubtitleTrack != "off" {
		if _, ok := trackIndex(req.SubtitleTrack, 's'); !ok {
			problem.Send(w, problem.InvalidRequest, "")
			return
		}
	}
	// The lowest of what the viewer asks, its device takes, our server's
	// cap and the pairing's.
	kbps := lowest(req.MaxBitrateKbps, req.Capabilities.MaxBitrateKbps, pb.MaxBitrateKbps, peer.Limits.BitrateKbps)
	if kbps == 0 {
		kbps = 120_000
	}
	pr.MaxStreamingBitrate = kbps * 1000
	// Its values exactly as MSFP writes them; another case is unknown.
	pr.DeviceProfile = playback.DeviceProfile(req.Capabilities.Exact(), pr.MaxStreamingBitrate)

	// The pairing's streams all taken: refused before asking Jellyfin
	// anything (Admit refuses it again, under its lock).
	if len(pb.Tickets.ForAccount(playback.PeerPrefix+peer.ID)) >= peer.Limits.Streams {
		problem.Send(w, problem.StreamLimitReached, "")
		return
	}
	ja, uid, ok := l.account(w, r)
	if !ok {
		return
	}
	// The pairing's streams and transcodes, and the server's transcodes
	// less those kept for our own people.
	transcodes := peer.Limits.Transcodes
	lim := playback.Limits{MaxStreams: peer.Limits.Streams, RefuseOverLimit: true, AccountTranscodes: &transcodes}
	if pb.MaxTranscodes > 0 {
		lim.MaxTranscodes = pb.MaxTranscodes - pb.PeerTranscodeReserve
		if lim.MaxTranscodes <= 0 {
			none := 0
			lim.AccountTranscodes, lim.MaxTranscodes = &none, 0
		}
	}
	id := playback.PeerPrefix + peer.ID
	o, ok := pb.open(w, r, openArgs{req: startRequest{AudioTrack: req.AudioTrack, SubtitleTrack: req.SubtitleTrack,
		StartPositionMs: req.StartPositionMs}, pr: pr, kbps: kbps, item: item, uid: uid, ja: ja,
		wantAudio: req.AudioLanguage, wantSubtitles: req.SubtitleLanguage, lim: lim, device: peer.Name,
		accountID: id, profileID: id, sessionID: id, urlBase: fed.Address, origin: origin(peer.Address), peerTracks: true,
		versionID: func(sourceID string) string { return "ver_" + fed.Key.OpaqueID("version", peer.ID, sourceID) }})
	if !ok {
		return
	}
	s, out := o.stream, o.out
	// The pairing ended, or the library is no longer offered, while it
	// opened: what ends its playbacks (PeerChanged) ran before this one
	// was admitted, so it ends here.
	if why := peerMayPlay(ctx, l.Provisioner.Store, peer.ID, s.ItemID); why != "" {
		pb.end(s, why, "")
		problem.Send(w, problem.NotFound, "")
		return
	}
	// Its seek-bar previews, a bonus, by the same ticket (MSFP section 10).
	out.Trickplay = pb.previews(ctx, s, ja, uid, func(n int) string {
		return fed.Address + "/api/v1/stream/" + s.Ticket + "/trickplay/" + strconv.Itoa(n)
	})
	if out.Trickplay != nil && !peerLayoutOK(out.Trickplay) {
		out.Trickplay, s.Trickplay = nil, nil // outside what MSFP allows: none
	}
	pb.Logger.Info("playback started for a paired server", "playback", s.ID, "peer", peer.Name, "method", o.dec.Method,
		"reasons", o.dec.Reasons(), "maxKbps", kbps)
	s.View = out
	// What plays, for the live view: the title (ours), the server playing
	// it showing as the account (the control panel).
	pb.Tickets.Describe(s, l.Provisioner.Store.ItemDescription(ctx, item))
	if err := l.Jellyfin.ReportStart(ctx, s.Auth, pb.state(s, pb.Tickets.Snapshot(s))); err != nil {
		pb.Logger.Warn("could not report a playback start", "playback", s.ID, "err", err)
	}
	fed.Answer(w, nonce, out)
}

// pickCopy is the copy whose audio has the language (its region, else the
// language alone), else the first.
func pickCopy(copies []store.PeerCopy, language string) string {
	if language != "" {
		for _, exact := range []bool{true, false} {
			for _, c := range copies {
				for _, a := range c.Audio {
					if (exact && a == language) || (!exact && lang.Base(a) == lang.Base(language)) {
						return c.Item
					}
				}
			}
		}
	}
	return copies[0].Item
}

type peerStopRequest struct {
	ID string `json:"id"`
	// PositionMs, optional: left out when not known (a playback stopped
	// after a restart here).
	PositionMs int64 `json:"positionMs,omitempty"`
}

// peerStop ends a paired server's viewer's playback: theirs only.
func (pb Playback) peerStop(w http.ResponseWriter, r *http.Request) {
	fed := pb.fed()
	if fed == nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	peer, body, nonce, ok := fed.PeerRequest(w, r)
	if !ok {
		return
	}
	var req peerStopRequest
	if federation.DecodeJSON(body, &req) != nil || !playIDRe.MatchString(req.ID) || req.PositionMs < 0 || req.PositionMs > maxPositionMs {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	if s, ended, ok := pb.Tickets.ByID(req.ID, playback.PeerPrefix+peer.ID, time.Now()); ok && ended == nil {
		// Without a position, where it last was stays.
		if req.PositionMs > 0 {
			pb.Tickets.Progress(s, req.PositionMs*10_000, false, time.Now())
		}
		pb.end(s, "stopped", "")
	}
	fed.Answer(w, nonce, struct{}{})
}

type peerProgressRequest struct {
	ID         string `json:"id"`
	PositionMs int64  `json:"positionMs"`
	Paused     bool   `json:"paused"`
}

// peerProgress is a paired server's viewer still watching, paused or not:
// their playback here isn't idle (IdleTimeout), and Jellyfin hears where
// it stands, as for this server's own.
func (pb Playback) peerProgress(w http.ResponseWriter, r *http.Request) {
	fed := pb.fed()
	if fed == nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	peer, body, nonce, ok := fed.PeerRequest(w, r)
	if !ok {
		return
	}
	var req peerProgressRequest
	if federation.DecodeJSON(body, &req) != nil || !playIDRe.MatchString(req.ID) || req.PositionMs < 0 || req.PositionMs > maxPositionMs {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	s, ended, ok := pb.Tickets.ByID(req.ID, playback.PeerPrefix+peer.ID, time.Now())
	if !ok || ended != nil {
		problem.Send(w, problem.PlaybackStopped, "")
		return
	}
	pb.Tickets.Progress(s, req.PositionMs*10_000, req.Paused, time.Now())
	if err := pb.Library.Jellyfin.ReportProgress(r.Context(), s.Auth, pb.state(s, pb.Tickets.Snapshot(s))); err != nil {
		pb.Logger.Warn("could not report progress", "playback", s.ID, "err", err)
	}
	fed.Answer(w, nonce, struct{}{})
}

// tellEvery is how often a paired server hears one of its playbacks is
// still watched here: well within its IdleTimeout.
const tellEvery = 45 * time.Second

// tellRemote tells a paired server, in the background, where a playback
// there is watched here: at most every tellEvery, and at once when it was
// paused or resumed.
func (pb Playback) tellRemote(rm *playback.Remote, positionMs int64, paused bool) {
	fed := pb.fed()
	if fed == nil {
		return
	}
	if !rm.Due(time.Now(), paused, tellEvery) {
		return
	}
	go func() {
		// On its schedule: held off while the paired server doesn't answer.
		ctx, cancel := context.WithTimeout(federation.Background(context.Background()), 15*time.Second)
		defer cancel()
		peer, err := pb.Library.Provisioner.Store.PeerByID(ctx, rm.PeerID)
		if err != nil {
			return
		}
		if _, err := fed.Call(ctx, peer, "/api/v1/federation/progress", peerProgressRequest{ID: rm.ID, PositionMs: positionMs, Paused: paused}, &struct{}{}); err != nil {
			pb.Logger.Info("a paired server couldn't be told a playback is still watched", "peer", peer.Name, "err", err)
		}
	}()
}

// origin is an address's origin, as browsers send it: scheme://host.
func origin(address string) string {
	u, err := url.Parse(address)
	if err != nil {
		return ""
	}
	return federation.Origin(u.Scheme + "://" + u.Host) // as browsers write it
}

// A title this server's people play from a paired server: no copy here
// they may play has their language, and a paired server's has (or there's
// no copy here at all). Overfin never picks by itself a source without
// the person's language when another has it (ROADMAP.md milestone 6).

// source is where a title plays from: one of our copies, or a paired
// server.
type source struct {
	item string // a Jellyfin item here
	peer *store.Peer
}

// sourceOf picks where a film or an episode plays for p, in language (a
// tag; "" for no preference): our copy with it, a paired server's with
// it, our copy, a paired server's. Ours are those p may see; paired
// servers' only for a profile not limited to some libraries, within its
// ceiling and hidden tags.
func (l Library) sourceOf(ctx context.Context, p *store.Profile, id, language string) (source, error) {
	ref, ok := titleRef(id)
	if !ok || (ref.Kind != "movie" && ref.Kind != "episode" && ref.Kind != "item") {
		return source{}, nil
	}
	st := l.Provisioner.Store
	infos, err := st.TitleInfos(ctx, []string{ref.Key()}, today(), "")
	if err != nil {
		return source{}, err
	}
	a, err := l.access(ctx, p)
	if err != nil {
		return source{}, err
	}
	var ours []string
	if t := infos[ref.Key()]; t != nil && len(t.Items) > 0 {
		if a == nil {
			ours = t.Items
		} else {
			allowed, _, err := st.AllowedItems(ctx, *a, t.Items)
			if err != nil {
				return source{}, err
			}
			for _, item := range t.Items {
				if allowed[item] {
					ours = append(ours, item)
				}
			}
		}
	}
	var peers []store.PeerSource
	if ref.Kind != "item" {
		if peers, err = l.peerSources(ctx, a, ref); err != nil {
			return source{}, err
		}
	}
	if language != "" {
		audio, err := st.ItemAudio(ctx, ours)
		if err != nil {
			return source{}, err
		}
		for _, item := range ours {
			if speaks(audio[item], language) {
				return source{item: item}, nil
			}
		}
		for _, ps := range peers {
			if speaks(ps.Audio, language) {
				return source{peer: ps.Peer}, nil
			}
		}
	}
	if len(ours) > 0 {
		return source{item: ours[0]}, nil
	}
	if len(peers) > 0 {
		return source{peer: peers[0].Peer}, nil
	}
	return source{}, nil
}

// speaks says whether audio has language: its region, or the language
// alone.
func speaks(audio []string, language string) bool {
	for _, a := range audio {
		if a == language || lang.Base(a) == lang.Base(language) {
			return true
		}
	}
	return false
}

// peerSources are the paired servers holding a film or an episode that a
// profile with access a may play: none when it's limited to some
// libraries or categories (paired servers' aren't among them); within its
// ceiling and its hidden tags, by the title's (or its series') TMDB ID.
func (l Library) peerSources(ctx context.Context, a *store.Access, ref store.TitleRef) ([]store.PeerSource, error) {
	if a.Limited() || ref.TMDB() == "" {
		return nil, nil
	}
	st := l.Provisioner.Store
	key := "movie/" + ref.TMDB()
	if ref.Kind != "movie" {
		key = "series/" + ref.TMDB()
	}
	if a != nil && a.Ceiling != nil {
		rated, err := st.TitlesAllowed(ctx, *a.Ceiling, []string{key})
		if err != nil || !rated[key] {
			return nil, err
		}
	}
	if a != nil && len(a.BlockedTags) > 0 {
		free, err := st.TitlesTagFree(ctx, a.BlockedTags, []string{key})
		if err != nil || !free[key] {
			return nil, err
		}
	}
	return st.PeerSources(ctx, ref.Key())
}

// startRemote starts a playback on a paired server for the caller, and
// keeps its profile's side here.
func (pb Playback) startRemote(w http.ResponseWriter, r *http.Request, caller *Caller, req startRequest, kbps int, peer *store.Peer, key string) {
	l, ctx, p := pb.Library, r.Context(), caller.Profile
	fed := pb.fed()
	if fed == nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	ref, _ := store.ParseTitleKey(key)
	tracksKey := ref.Key()
	if ref.Kind == "episode" {
		tracksKey = ref.Series().Key()
	}
	wantAudio, wantSubtitles := l.wishesFor(ctx, p, tracksKey, req.AudioLanguage)
	var out sessionJSON
	// Another audio track or a quality restarts a playback, and the
	// browser's stop of the old one may come after this start: this
	// browser's playback of the title there ends first, so the other
	// server doesn't count it against the pairing's limit, and the new
	// one waits for that stop (remoteStops).
	for _, old := range pb.Tickets.Where(func(s *playback.Stream) bool {
		return s.Remote != nil && s.Remote.PeerID == peer.ID && s.Remote.Key == key &&
			s.SessionID == caller.Session.ID && s.ProfileID == p.ID
	}) {
		pb.end(old, "stopped", "")
	}
	remoteStops.wait(ctx, peer.ID, 5*time.Second)
	reply, err := fed.CallReply(ctx, peer, "/api/v1/federation/play", peerPlayRequest{Title: key, Capabilities: req.Capabilities.ForPeer(),
		StartPositionMs: req.StartPositionMs, MaxBitrateKbps: roundKbps(kbps), AudioLanguage: wantAudio, SubtitleLanguage: wantSubtitles,
		AudioTrack: req.AudioTrack, SubtitleTrack: req.SubtitleTrack}, &out)
	if errors.Is(err, federation.ErrBackingOff) {
		// It asked to wait: busy now, the viewer may try again later.
		problem.Send(w, problem.ServerBusy, "")
		return
	}
	if err != nil {
		// No answer, or none signed (its host's own 429 among them):
		// busy for now, as MSFP core section 8.3 says; only its signed
		// refusal says a title isn't playable.
		pb.Logger.Warn("a paired server couldn't start a playback", "peer", peer.Name, "err", err)
		problem.Send(w, problem.ServerBusy, "")
		return
	}
	if reply.Status != http.StatusOK {
		problem.Send(w, peerRefusal(reply), "")
		return
	}
	// Its URLs are its stream's, on its address, nothing else; its words
	// are plain text, as every other's (rule 15).
	base, ok := keepPeerURLs(&out, peer.Address)
	if !ok || !cleanPeerText(&out) {
		why := "values Overfin doesn't give"
		if !ok {
			why = "other addresses"
		}
		pb.Logger.Warn("a paired server answered a playback with "+why, "peer", peer.Name)
		pb.stopRemote(peer, out.ID, 0)
		problem.Send(w, problem.NotPlayable, "")
		return
	}
	titles, err := l.titleSummaries(ctx, p, []string{key})
	if err != nil {
		pb.stopRemote(peer, out.ID, 0)
		l.storeError(w, err)
		return
	}
	device := req.Device
	if device == "" {
		device = deviceName(r.UserAgent())
	}
	lim := playback.Limits{MaxStreams: caller.Account.Template.MaxStreams, RefuseOverLimit: pb.RefuseOverLimit}
	if lim.MaxStreams > 0 && caller.Account.Template.ReserveStreams {
		profiles, err := l.Provisioner.Store.Profiles(ctx, p.AccountID)
		if err != nil {
			pb.stopRemote(peer, out.ID, 0)
			problem.Send(w, problem.Internal, "")
			return
		}
		lim.Reserve, lim.Profiles = true, len(profiles)
	}
	s := &playback.Stream{AccountID: p.AccountID, ProfileID: p.ID, SessionID: caller.Session.ID, Device: device,
		Decision: playback.Decision{Method: "remote", Source: jellyfin.MediaSource{RunTimeTicks: out.DurationMs * 10_000}},
		Remote:   &playback.Remote{PeerID: peer.ID, ID: out.ID, Key: key, Base: base}, Trickplay: layoutOf(out.Trickplay)}
	replaced, err := pb.Tickets.Admit(s, req.StartPositionMs*10_000, time.Now(), lim, device)
	if err != nil {
		pb.stopRemote(peer, out.ID, 0)
		problem.Send(w, problem.StreamLimitReached, "")
		return
	}
	for _, old := range replaced {
		pb.stopped(old)
	}
	// Remembered until it ends, so that a restart here tells the paired
	// server it's over (StopLeftovers).
	if err := l.Provisioner.Store.AddRemotePlayback(ctx, peer.ID, s.Remote.ID); err != nil {
		pb.Logger.Warn("a playback on a paired server couldn't be remembered", "err", err)
	}
	// The browser asks this server only (rule 97).
	relayURLs(&out, base, s.Ticket)
	out.ID, out.Title, out.Next = s.ID, nil, nil
	// Its previews' sheets, through this server too.
	if tp := out.Trickplay; tp != nil {
		for n := range tp.Sheets {
			tp.Sheets[n] = "/api/v1/playback/" + s.ID + "/trickplay/" + strconv.Itoa(n)
		}
	}
	if len(titles) == 1 {
		out.Title = &titles[0]
		if ref.Kind == "episode" {
			if next, err := l.nextEpisode(ctx, p, ref); err == nil {
				out.Next = next
			}
		}
	}
	pb.Logger.Info("playback started on a paired server", "playback", s.ID, "peer", peer.Name, "method", out.Method)
	s.View = out
	pb.Tickets.Describe(s, describe(out.Title))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeOK(w, out)
}

// peerRefusal is what the viewer is told of a paired server's refusal to
// play: its limit for this server reached, or all its transcodes taken, is
// busy, for now (409 stream-limit-reached, 503, and 429 when it's asked
// too often); a title it can't play
// (409 not-playable), or any other refusal, isn't playable. A 409 of
// another type, or none, is busy: what every 409 meant to Overfin's
// viewer side before it read the type.
func peerRefusal(r federation.Reply) problem.Type {
	switch {
	case r.Status == http.StatusConflict && r.Problem == string(problem.NotPlayable):
		return problem.NotPlayable
	case r.Status == http.StatusConflict, r.Status == http.StatusServiceUnavailable, r.Status == http.StatusTooManyRequests:
		return problem.ServerBusy
	}
	return problem.NotPlayable
}

// keepPeerURLs checks a paired server's playback: its stream's,
// subtitles' and previews' sheets' URLs must be files of one stream of its
// own, at its address. It gives that stream's base, up to its ticket and
// "/".
func keepPeerURLs(out *sessionJSON, address string) (string, bool) {
	prefix := strings.TrimSuffix(address, "/") + "/api/v1/stream/"
	ticket, _, found := strings.Cut(strings.TrimPrefix(out.Stream.URL, prefix), "/")
	if !strings.HasPrefix(out.Stream.URL, prefix) || !found || !ticketRe.MatchString(ticket) {
		return "", false
	}
	base := prefix + ticket + "/"
	ok := func(u string) bool {
		rest, in := strings.CutPrefix(u, base)
		name, track, sub := strings.Cut(rest, "/")
		if sub {
			return in && (name == "subtitles" || name == "pictures") && fileName(track)
		}
		return in && fileName(rest)
	}
	if !ok(out.Stream.URL) || (out.Stream.Protocol != "hls" && out.Stream.Protocol != "file") || !playIDRe.MatchString(out.ID) {
		return "", false
	}
	for _, list := range [][]trackJSON{out.SubtitleTracks, out.ForcedTracks} {
		for _, t := range list {
			if t.URL != "" && !ok(t.URL) {
				return "", false
			}
		}
	}
	// Sheet n of its previews is at "trickplay/n" of the same stream.
	if tp := out.Trickplay; tp != nil {
		for n, u := range tp.Sheets {
			if u != base+"trickplay/"+strconv.Itoa(n) {
				return "", false
			}
		}
	}
	return base, true
}

// Bounds on a paired server's seek-bar previews (MSFP section 9.5): tiles
// and sheets of sizes this server re-encodes (images.MaxDimension), a
// sheet count that a film's previews never need more than.
const (
	maxPeerTile      = 2048
	maxPeerGrid      = 100
	maxPeerSheetSide = 4096
	maxPeerSheets    = 1000
	minPeerInterval  = 100
	maxPeerInterval  = 3_600_000
)

// peerLayoutOK says whether a paired server's previews are within those
// bounds, one sheet listed for each the tiles fill.
func peerLayoutOK(tp *trickplayJSON) bool {
	in := func(v, lo, hi int) bool { return v >= lo && v <= hi }
	if !in(tp.TileWidth, 1, maxPeerTile) || !in(tp.TileHeight, 1, maxPeerTile) || !in(tp.Columns, 1, maxPeerGrid) ||
		!in(tp.Rows, 1, maxPeerGrid) || tp.Columns*tp.TileWidth > maxPeerSheetSide || tp.Rows*tp.TileHeight > maxPeerSheetSide ||
		!in(tp.IntervalMs, minPeerInterval, maxPeerInterval) || !in(tp.TileCount, 1, maxPeerSheets*tp.Columns*tp.Rows) {
		return false
	}
	return len(tp.Sheets) == sheetCount(*layoutOf(tp))
}

// layoutOf is a set of previews' layout, as a playback keeps it.
func layoutOf(tp *trickplayJSON) *jellyfin.Trickplay {
	if tp == nil {
		return nil
	}
	return &jellyfin.Trickplay{Width: tp.TileWidth, Height: tp.TileHeight, Columns: tp.Columns, Rows: tp.Rows,
		Count: tp.TileCount, IntervalMs: tp.IntervalMs}
}

// relayURLs gives a paired server's playback this server's addresses: its
// files under the ticket this server gave, the paired server's left out.
func relayURLs(out *sessionJSON, base, ticket string) {
	local := func(u string) string {
		if u == "" {
			return ""
		}
		return "/api/v1/stream/" + ticket + "/" + strings.TrimPrefix(u, base)
	}
	out.Stream.URL = local(out.Stream.URL)
	for _, list := range [][]trackJSON{out.SubtitleTracks, out.ForcedTracks} {
		for i := range list {
			list[i].URL = local(list[i].URL)
		}
	}
}

// peerLabel is a paired server's label as one line of plain text.
func peerLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, textclean.Plain(s, 64))
	return strings.Join(strings.Fields(s), " ")
}

// cleanPeerText keeps of a paired server's playback answer only plain
// text, and values Overfin itself gives; false when its method isn't one.
func cleanPeerText(out *sessionJSON) bool {
	switch out.Method {
	case "direct", "remux", "transcode":
	default:
		return false
	}
	switch out.Stream.MimeType {
	case "", "application/vnd.apple.mpegurl":
	default:
		out.Stream.MimeType = ""
	}
	clean := func(list []trackJSON) []trackJSON {
		kept := list[:0]
		for _, t := range list {
			if !peerIDRe.MatchString(t.ID) {
				continue
			}
			t.Label = peerLabel(t.Label)
			t.Language = languageTag(t.Language)
			if t.Role != tracks.Commentary && t.Role != tracks.Description {
				t.Role = ""
			}
			if t.Label != "" {
				kept = append(kept, t)
			}
		}
		return kept
	}
	out.AudioTracks, out.SubtitleTracks, out.ForcedTracks = clean(out.AudioTracks), clean(out.SubtitleTracks), clean(out.ForcedTracks)
	// Its subtitles go in stretches of this server's length: both run the
	// same version.
	out.SubtitleStretchSeconds = 0
	if len(out.SubtitleTracks)+len(out.ForcedTracks) > 0 {
		out.SubtitleStretchSeconds = StretchSeconds
	}
	// IDs as this server's own look, the duration a duration, skip markers
	// of the kinds there are, within the title (rules 15 and 96).
	for _, id := range []*string{&out.VersionID, &out.AudioTrack, &out.SubtitleTrack, &out.ForcedTrack} {
		if !peerIDRe.MatchString(*id) && *id != "off" {
			*id = ""
		}
	}
	if out.DurationMs < 0 || out.DurationMs > maxPositionMs {
		out.DurationMs = 0
	}
	segments := out.Segments[:0]
	for _, sg := range out.Segments {
		if slices.Contains(segmentKinds, sg.Kind) && sg.StartMs >= 0 && sg.EndMs > sg.StartMs && sg.EndMs <= maxPositionMs && len(segments) < 32 {
			segments = append(segments, sg)
		}
	}
	out.Segments = segments
	versions := out.Versions[:0]
	for _, v := range out.Versions {
		if !peerIDRe.MatchString(v.ID) {
			continue
		}
		if v.Name = peerLabel(v.Name); v.Name != "" {
			versions = append(versions, v)
		}
	}
	out.Versions = versions
	// Previews out of bounds are none: the playback goes on without them.
	if out.Trickplay != nil && !peerLayoutOK(out.Trickplay) {
		out.Trickplay = nil
	}
	if in := out.Info; in != nil {
		in.Container = textclean.Plain(in.Container, 32)
		for _, st := range []*streamInfoJSON{in.Video, in.Audio} {
			if st != nil {
				st.Codec, st.Profile, st.Range = textclean.Plain(st.Codec, 32), textclean.Plain(st.Profile, 32), textclean.Plain(st.Range, 32)
				st.Language = languageTag(st.Language)
			}
		}
		reasons := in.Reasons[:0]
		for _, r := range in.Reasons {
			if r = textclean.Plain(r, 64); r != "" {
				reasons = append(reasons, r)
			}
		}
		in.Reasons = reasons
	}
	return true
}

// stopRemote tells a paired server a playback there is over, in the
// background.
func (pb Playback) stopRemote(peer *store.Peer, id string, positionMs int64) {
	fed := pb.fed()
	if fed == nil {
		return
	}
	done := remoteStops.begin(peer.ID)
	go func() {
		defer done()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := fed.Call(ctx, peer, "/api/v1/federation/stop", peerStopRequest{ID: id, PositionMs: positionMs}, &struct{}{}); err != nil {
			pb.Logger.Info("a paired server couldn't be told a playback stopped", "peer", peer.Name, "err", err)
		}
		if err := pb.Library.Provisioner.Store.ForgetRemotePlayback(ctx, peer.ID, id); err != nil {
			pb.Logger.Warn("a playback on a paired server couldn't be forgotten", "err", err)
		}
	}()
}

// StopLeftovers tells paired servers the playbacks this server had there
// before it started are over: playbacks live in memory, and a paired
// server would otherwise hold them, counted against the pairing's
// streams, until its idle timeout. Run once at start, before anyone plays.
func (pb Playback) StopLeftovers(ctx context.Context) {
	st := pb.Library.Provisioner.Store
	left, err := st.TakeRemotePlaybacks(ctx)
	if err != nil {
		pb.Logger.Warn("playbacks on paired servers couldn't be read", "err", err)
		return
	}
	for _, rp := range left {
		peer, err := st.PeerByID(ctx, rp.PeerID)
		if err != nil || peer.State != store.PeerPaired {
			continue
		}
		pb.stopRemote(peer, rp.RemoteID, 0)
	}
}

// remoteStops are the stops on their way to each paired server. A new
// playback there waits for them a moment first: another audio track or a
// quality restarts a playback (stop, then start), and the other server,
// counting the old one still, would refuse the new one over its limit.
var remoteStops = pendingStops{byPeer: map[string]int{}}

type pendingStops struct {
	mu     sync.Mutex
	byPeer map[string]int
}

func (p *pendingStops) begin(peer string) (done func()) {
	p.mu.Lock()
	p.byPeer[peer]++
	p.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			if p.byPeer[peer]--; p.byPeer[peer] <= 0 {
				delete(p.byPeer, peer)
			}
			p.mu.Unlock()
		})
	}
}

// wait waits for the stops on their way to peer, at most limit.
func (p *pendingStops) wait(ctx context.Context, peer string, limit time.Duration) {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		p.mu.Lock()
		n := p.byPeer[peer]
		p.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// chooseRemoteTracks keeps the tracks chosen while watching a paired
// server's copy, for the title, by their languages as it listed them.
func (pb Playback) chooseRemoteTracks(w http.ResponseWriter, r *http.Request, s *playback.Stream, audio, subtitles *string) {
	ctx, st := r.Context(), pb.Library.Provisioner.Store
	view, _ := s.View.(sessionJSON)
	ref, ok := store.ParseTitleKey(s.Remote.Key)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	key := ref.Key()
	if ref.Kind == "episode" {
		key = ref.Series().Key()
	}
	find := func(list []trackJSON, id string) (trackJSON, bool) {
		for _, t := range list {
			if t.ID == id {
				return t, true
			}
		}
		return trackJSON{}, false
	}
	var err error
	if audio != nil {
		// The owner's words for the language, kept only when they're one:
		// what's kept here for the profile is never a peer's free text.
		if t, ok := find(view.AudioTracks, *audio); ok && languageTag(t.Language) != "" && t.Role == "" {
			err = st.SetTitleAudio(ctx, s.ProfileID, key, t.Language)
		}
	}
	if err == nil && subtitles != nil {
		if *subtitles == "" {
			err = st.SetTitleSubtitles(ctx, s.ProfileID, key, "")
		} else if t, ok := find(view.SubtitleTracks, *subtitles); ok && languageTag(t.Language) != "" {
			err = st.SetTitleSubtitles(ctx, s.ProfileID, key, t.Language)
		}
	}
	if err != nil {
		pb.Library.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// peerIDRe is what a paired server's IDs (tracks, versions) may be: plain
// and short, as this server's own are.
var peerIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// segmentKinds are the skip markers there are (Jellyfin's, in lower case).
var segmentKinds = []string{"intro", "outro", "recap", "preview", "commercial"}

// roundKbps is a bitrate as a paired server is told it: in whole Mbps
// from 1 Mbps up (an exact number could tell one viewer's device from
// another), never 0, which would mean no cap.
func roundKbps(kbps int) int {
	if kbps < 1000 {
		return kbps
	}
	return kbps / 1000 * 1000
}

// peerMayPlay says why a paired server may no longer play an item here
// ("pairing ended", "no longer shared"), "" when it may (or it couldn't be
// told: each media request checks again).
func peerMayPlay(ctx context.Context, st *store.Store, peerID, itemID string) string {
	peer, err := st.PeerByID(ctx, peerID)
	switch {
	case errors.Is(err, store.ErrNotFound), err == nil && peer.State != store.PeerPaired:
		return "pairing ended"
	case err != nil:
		return ""
	}
	if offered, err := st.ItemOfferedTo(ctx, peerID, itemID); err == nil && !offered {
		return "no longer shared"
	}
	return ""
}

// PeerChanged ends at once what a paired server may no longer play here,
// after its pairing ended or changed: all its playbacks when it is no
// longer paired, those of a library no longer offered to it otherwise; and
// this server's own people's playbacks from it, when it is no longer
// paired. A ticket stops working then, not at its next request (MSFP
// playback, section 6.3).
func (pb Playback) PeerChanged(peerID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	st := pb.Library.Provisioner.Store
	peer, err := st.PeerByID(ctx, peerID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		pb.Logger.Warn("a paired server's playbacks couldn't be checked", "err", err)
		return
	}
	paired := err == nil && peer.State == store.PeerPaired
	for _, s := range pb.Tickets.Where(func(s *playback.Stream) bool { return s.ProfileID == playback.PeerPrefix+peerID }) {
		if why := peerMayPlay(ctx, st, peerID, s.ItemID); why != "" {
			pb.end(s, why, "")
		}
	}
	if !paired {
		for _, s := range pb.Tickets.Where(func(s *playback.Stream) bool { return s.Remote != nil && s.Remote.PeerID == peerID }) {
			pb.end(s, "pairing ended", "")
		}
	}
}
