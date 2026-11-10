package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"time"

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
// one), and answer with a playback whose stream the viewer's browser
// fetches straight from here, with its ticket only. Everything personal
// (what they watch, where they are) stays on their server.

type peerPlayRequest struct {
	// Title is the title key: a film's or an episode's.
	Title           string                `json:"title"`
	Capabilities    playback.Capabilities `json:"capabilities"`
	StartPositionMs int64                 `json:"startPositionMs"`
	MaxBitrateKbps  int                   `json:"maxBitrateKbps"`
	// AudioLanguage and SubtitleLanguage are the viewer's, as tags: the
	// copy and the tracks are picked by them.
	AudioLanguage    string `json:"audioLanguage"`
	SubtitleLanguage string `json:"subtitleLanguage"`
	AudioTrack       string `json:"audioTrack"`
	SubtitleTrack    string `json:"subtitleTrack"`
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
	if json.Unmarshal(body, &req) != nil {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	ref, known := store.ParseTitleKey(req.Title)
	if !known || (ref.Kind != "movie" && ref.Kind != "episode") || req.StartPositionMs < 0 || req.MaxBitrateKbps < 0 ||
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
	if req.AudioTrack != "" {
		n, ok := trackIndex(req.AudioTrack, 'a')
		if !ok {
			problem.Send(w, problem.InvalidRequest, "")
			return
		}
		pr.AudioStreamIndex = &n
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
	pr.DeviceProfile = playback.DeviceProfile(req.Capabilities, pr.MaxStreamingBitrate)

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
		accountID: id, profileID: id, sessionID: id, urlBase: fed.Address, origin: origin(peer.Address)})
	if !ok {
		return
	}
	s, out := o.stream, o.out
	pb.Logger.Info("playback started for a paired server", "playback", s.ID, "peer", peer.Name, "method", o.dec.Method,
		"reasons", o.dec.Reasons(), "maxKbps", kbps)
	s.View = out
	pb.Tickets.Describe(s, peer.Name)
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
	ID         string `json:"id"`
	PositionMs int64  `json:"positionMs"`
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
	if json.Unmarshal(body, &req) != nil || !playIDRe.MatchString(req.ID) || req.PositionMs < 0 {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	if s, ended, ok := pb.Tickets.ByID(req.ID, playback.PeerPrefix+peer.ID, time.Now()); ok && ended == nil {
		pb.Tickets.Progress(s, req.PositionMs*10_000, false, time.Now())
		pb.end(s, "stopped", "")
	}
	fed.Answer(w, nonce, struct{}{})
}

// origin is an address's origin, as browsers send it: scheme://host.
func origin(address string) string {
	u, err := url.Parse(address)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
