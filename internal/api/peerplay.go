package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/msfp-io/msfp-go"
	"github.com/msfp-io/msfp-go/wire"
	"github.com/overfin/server/internal/copies"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/lang"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
)

// engine is federation now, the MSFP engine: nil without it.
func (pb Playback) engine() *msfp.Engine {
	if pb.Engine == nil {
		return nil
	}
	return pb.Engine()
}

// A title this server's people play from a paired server: no copy here
// they may play has their language, and a paired server's has (or there's
// no copy here at all). Overfin never picks by itself a source without
// the person's language when another has it (ROADMAP.md milestone 6).

// source is where a title plays from: one of our copies, or a paired
// server (what it sent of the title in theirs).
type source struct {
	item   string // a Jellyfin item here
	peer   string // a paired server's ID
	theirs *store.PeerSource
}

// sourcesOf lists where a film or an episode may play for p, in language
// (a tag; "" for no preference), in the order playing picks: our copies
// with it, paired servers' with it, our other copies, other paired
// servers'. Ours are those p may see; paired servers' only for a profile
// not limited to some libraries, within its ceiling and hidden tags, in
// the order they were paired. Each copy and each paired server once.
func (l Library) sourcesOf(ctx context.Context, p *store.Profile, id, language string) ([]source, error) {
	ref, ok := titleRef(id)
	if !ok || (ref.Kind != "movie" && ref.Kind != "episode" && ref.Kind != "item") {
		return nil, nil
	}
	st := l.Provisioner.Store
	infos, err := st.TitleInfos(ctx, []string{ref.Key()}, today(), "")
	if err != nil {
		return nil, err
	}
	a, err := l.access(ctx, p)
	if err != nil {
		return nil, err
	}
	var ours []string
	if t := infos[ref.Key()]; t != nil && len(t.Items) > 0 {
		if a == nil {
			ours = t.Items
		} else {
			allowed, _, err := st.AllowedItems(ctx, *a, t.Items)
			if err != nil {
				return nil, err
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
			return nil, err
		}
	}
	audio := map[string][]string{}
	if language != "" {
		if audio, err = st.ItemAudio(ctx, ours); err != nil {
			return nil, err
		}
	}
	var out []source
	for _, with := range []bool{true, false} {
		for _, item := range ours {
			if (language != "" && speaks(audio[item], language)) == with {
				out = append(out, source{item: item})
			}
		}
		for i, ps := range peers {
			if (language != "" && speaks(ps.Audio, language)) == with {
				out = append(out, source{peer: ps.Peer, theirs: &peers[i]})
			}
		}
	}
	return out, nil
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

// A paired server's copy as this server's players see it: MSFP's copy
// object as the paired server sent it (catalog), its ID Overfin's own
// (peerCopyID), so that players see one kind of ID, and mapped back here
// to the paired server, the title's key there and its version ID.
type peerCopy struct {
	ID     string
	Source store.PeerSource
	Copy   wire.Copy // as received, its ID the paired server's version ID
}

// peerCopyID is the ID this server's players know a paired server's copy
// by: a keyed hash of the pairing, the title's key there and its version
// ID (copies.PeerCopyID).
func (l Library) peerCopyID(peer, key, version string) string {
	return copies.PeerCopyID(l.CopyKey, peer, key, version)
}

// peerCopies are the paired servers' copies of t the profile may see
// (peerSources), in the order the servers were paired, each server's in
// its own order (its preference).
func (l Library) peerCopies(ctx context.Context, p *store.Profile, t *store.TitleInfo) ([]peerCopy, error) {
	if t == nil || l.CopyKey == nil || (t.Ref.Kind != "movie" && t.Ref.Kind != "episode") {
		return nil, nil
	}
	a, err := l.access(ctx, p)
	if err != nil {
		return nil, err
	}
	sources, err := l.peerSources(ctx, a, t.Ref)
	if err != nil {
		return nil, err
	}
	var out []peerCopy
	for _, ps := range sources {
		for _, c := range ps.Copies {
			out = append(out, peerCopy{ID: l.peerCopyID(ps.Peer, ps.Key, c.ID), Source: ps, Copy: c})
		}
	}
	return out, nil
}

// playerCopy is a paired server's copy as this server's players get it:
// its facts as received, its ID Overfin's.
func (pc peerCopy) playerCopy() wire.Copy {
	w := pc.Copy
	w.ID = pc.ID
	if w.AudioTracks == nil {
		w.AudioTracks = []wire.CopyAudio{}
	}
	return w
}

// remotePlay is a play on a paired server, as this server asks it: the
// copy, MSFP's request as the player made it (the copy and the title
// named the paired server's way here), and Overfin's members.
type remotePlay struct {
	copy   peerCopy
	req    wire.PlayRequest
	key    string // the title's key here
	device string
	reason string
}

// playRemote starts a playback on a paired server for the caller, and
// keeps its profile's side here: the MSFP engine asks the paired server
// (msfp.Engine.Play) with the player's request almost as it is (the
// version ID mapped back), and checks its answer; the paired server's
// ticket stays with the engine, and the player gets this server's own,
// under which every URL is this server's (rule 97). It answers w itself.
func (pb Playback) playRemote(w http.ResponseWriter, r *http.Request, caller *Caller, rp remotePlay) (*playback.Stream, msfp.RemotePlayback, bool) {
	ctx, p := r.Context(), caller.Profile
	eng := pb.engine()
	if eng == nil {
		problem.Send(w, problem.NotFound, "")
		return nil, msfp.RemotePlayback{}, false
	}
	peer, pc := rp.copy.Source.Peer, rp.copy
	// Another audio track or a quality restarts a playback, and the
	// browser's stop of the old one may come after this start: this
	// browser's playback of the title there ends first, the engine
	// stopping it there before it asks for the new one (Replaces), so the
	// other server doesn't count it against the pairing's limit.
	replaces := ""
	for _, old := range pb.Tickets.Where(func(s *playback.Stream) bool {
		return s.Remote != nil && s.Remote.PeerID == peer && s.Remote.Key == rp.key &&
			s.SessionID == caller.Session.ID && s.ProfileID == p.ID
	}) {
		old.Remote.Replaced.Store(true)
		replaces = old.Remote.ID()
		pb.end(old, "stopped", "")
	}
	kbps := rp.req.BitrateCap(pb.MaxBitrateKbps, 0)
	got, err := eng.Play(ctx, peer, msfp.PlayRequest{Title: pc.Source.Key, Purpose: rp.req.Purpose, Delivery: rp.req.Delivery,
		MediaType: rp.req.MediaType, Transcode: rp.req.Transcode, AudioRenditions: rp.req.AudioRenditions,
		StartMs: rp.req.StartPositionMs, MaxBitrateKbps: kbps, VersionID: pc.Copy.ID, AudioTrack: rp.req.AudioTrack,
		Replaces: replaces})
	if err != nil {
		pb.Logger.Info("a paired server didn't start a playback", "peer", peer, "err", err)
		sendRemoteError(w, err)
		return nil, msfp.RemotePlayback{}, false
	}
	stop := func() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			eng.Stop(ctx, got.ID, -1)
		}()
	}
	if got.CopyChanged {
		// The paired server's copy isn't the one its catalog said: the
		// engine reads the title again; this play goes on (its answer
		// wins), the player told only if it fails.
		pb.Logger.Info("a paired server's copy changed since its catalog was read", "peer", peer)
	}
	lim, err := pb.limitsOf(ctx, caller.Account.Template, p.AccountID)
	if err != nil {
		stop()
		problem.Send(w, problem.Internal, "")
		return nil, msfp.RemotePlayback{}, false
	}
	lim.MaxTranscodes = 0 // converted there, not here
	device := rp.device
	if device == "" {
		device = deviceName(r.UserAgent())
	}
	s := &playback.Stream{AccountID: p.AccountID, ProfileID: p.ID, SessionID: caller.Session.ID, Device: device,
		Reason: rp.reason,
		Remote: playback.NewRemote(peer, rp.key, pc.Copy, got)}
	if pc.Copy.PreviewLayout != nil && got.Sheets > 0 {
		pl := pc.Copy.PreviewLayout
		s.Trickplay = &jellyfin.Trickplay{Width: pl.TileWidth, Height: pl.TileHeight, Columns: pl.Columns, Rows: pl.Rows,
			Count: pl.TileCount, IntervalMs: pl.IntervalMs}
	}
	replaced, err := pb.Tickets.Admit(s, got.PositionMs*10_000, time.Now(), lim, device)
	if err != nil {
		stop()
		problem.Send(w, problem.StreamLimitReached, "")
		return nil, msfp.RemotePlayback{}, false
	}
	for _, old := range replaced {
		pb.stopped(old, "taken-over")
	}
	pb.Logger.Info("playback started on a paired server", "playback", s.ID, "peer", peer, "method", got.Method,
		"delivery", rp.req.Delivery)
	return s, got, true
}

// sendRemoteError answers a play a paired server didn't start: busy now
// (its limit for this server reached, all its transcodes taken, no
// answer, or asked to wait: MSFP core section 8.3), the delivery refused
// (the player steps down), the copy gone (the player reads the title
// again); anything else isn't playable.
func sendRemoteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, msfp.ErrServerBusy), errors.Is(err, msfp.ErrStreamLimitReached), errors.Is(err, msfp.ErrBackingOff):
		problem.Send(w, problem.ServerBusy, "")
	case errors.Is(err, msfp.ErrDeliveryRefused):
		problem.Send(w, problem.DeliveryRefused, "peer")
	case errors.Is(err, msfp.ErrNotFound), errors.Is(err, msfp.ErrCopyChanged):
		problem.Send(w, problem.NotFound, "")
	default:
		problem.Send(w, problem.NotPlayable, "")
	}
}

// RemoteEnded ends this server's side of a playback on a paired server
// that ended there, or with the pairing (msfp.RemotePlaybackEnded): the
// viewer's player stops.
func (pb Playback) RemoteEnded(id string) {
	for _, s := range pb.Tickets.Where(func(s *playback.Stream) bool { return s.Remote != nil && s.Remote.ID() == id }) {
		s.Remote.Replaced.Store(true) // over there already
		pb.end(s, "pairing ended", "")
	}
}

// chooseRemoteTracks keeps the tracks chosen while watching a paired
// server's copy, for the title, by their languages as its copy lists them
// (tags only: never a paired server's free text).
func (pb Playback) chooseRemoteTracks(w http.ResponseWriter, r *http.Request, s *playback.Stream, audio, subtitles *string) {
	ctx, st := r.Context(), pb.Library.Provisioner.Store
	ref, ok := store.ParseTitleKey(s.Remote.Key)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	key := ref.Key()
	if ref.Kind == "episode" {
		key = ref.Series().Key()
	}
	c := s.Remote.Copy
	var err error
	if audio != nil {
		for _, t := range c.AudioTracks {
			if t.ID == *audio && languageTag(t.Language) != "" && t.Role == "" {
				err = st.SetTitleAudio(ctx, s.ProfileID, key, t.Language)
			}
		}
	}
	if err == nil && subtitles != nil {
		if *subtitles == "" {
			err = st.SetTitleSubtitles(ctx, s.ProfileID, key, "")
		} else {
			for _, t := range c.SubtitleTracks {
				if t.ID == *subtitles && languageTag(t.Language) != "" {
					err = st.SetTitleSubtitles(ctx, s.ProfileID, key, t.Language)
				}
			}
		}
	}
	if err != nil {
		pb.Library.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
