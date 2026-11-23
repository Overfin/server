package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/msfp-io/msfp-go"
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

// sourceOf picks where a film or an episode plays for p, in language (a
// tag; "" for no preference): the first of sourcesOf, none when there is
// none.
func (l Library) sourceOf(ctx context.Context, p *store.Profile, id, language string) (source, error) {
	all, err := l.sourcesOf(ctx, p, id, language)
	if err != nil || len(all) == 0 {
		return source{}, err
	}
	return all[0], nil
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
	if !FederationOn {
		// Off until plan phase 7: a peer's title kept from before isn't
		// a source.
		return nil, nil
	}
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
// keeps its profile's side here: the MSFP engine asks the paired server
// (msfp.Engine.Play) and checks its answer; this server gives the viewer
// its own addresses for everything, and its own title and next episode.
func (pb Playback) startRemote(w http.ResponseWriter, r *http.Request, caller *Caller, req startRequest, kbps int, peer, key string) {
	l, ctx, p := pb.Library, r.Context(), caller.Profile
	eng := pb.engine()
	if eng == nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	ref, _ := store.ParseTitleKey(key)
	// msfp-go's revised v0 takes no capabilities nor languages: this
	// server chooses the copy, its audio and the delivery from the copy's
	// facts. Until plan phase 7 does (federation is off on this branch:
	// pb.engine() is nil), the play names only what the player named.
	// Another audio track or a quality restarts a playback, and the
	// browser's stop of the old one may come after this start: this
	// browser's playback of the title there ends first, the engine
	// stopping it there before it asks for the new one (Replaces), so the
	// other server doesn't count it against the pairing's limit.
	replaces := ""
	for _, old := range pb.Tickets.Where(func(s *playback.Stream) bool {
		return s.Remote != nil && s.Remote.PeerID == peer && s.Remote.Key == key &&
			s.SessionID == caller.Session.ID && s.ProfileID == p.ID
	}) {
		old.Remote.Replaced.Store(true)
		replaces = old.Remote.ID
		pb.end(old, "stopped", "")
	}
	rp, err := eng.Play(ctx, peer, msfp.PlayRequest{Title: key, StartMs: req.StartPositionMs, MaxBitrateKbps: kbps,
		AudioTrack: req.AudioTrack, VersionID: req.VersionID, Replaces: replaces})
	if err != nil {
		// Busy now, the viewer may try again later: its limit for this
		// server reached, all its transcodes taken, no answer, or asked to
		// wait (MSFP core section 8.3); anything else isn't playable.
		why := problem.NotPlayable
		switch {
		case errors.Is(err, msfp.ErrServerBusy), errors.Is(err, msfp.ErrStreamLimitReached), errors.Is(err, msfp.ErrBackingOff):
			why = problem.ServerBusy
		}
		pb.Logger.Info("a paired server didn't start a playback", "peer", peer, "err", err)
		problem.Send(w, why, "")
		return
	}
	stop := func() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			eng.Stop(ctx, rp.ID, -1)
		}()
	}
	titles, err := l.titleSummaries(ctx, p, []string{key})
	if err != nil {
		stop()
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
			stop()
			problem.Send(w, problem.Internal, "")
			return
		}
		lim.Reserve, lim.Profiles = true, len(profiles)
	}
	s := &playback.Stream{AccountID: p.AccountID, ProfileID: p.ID, SessionID: caller.Session.ID, Device: device,
		Decision: playback.Decision{Method: "remote", Source: jellyfin.MediaSource{RunTimeTicks: rp.DurationMs * 10_000}},
		Remote:   &playback.Remote{PeerID: peer, ID: rp.ID, Key: key}}
	// Previews: the copy's layout and Engine.PreviewSheet in plan phase 7.
	replaced, err := pb.Tickets.Admit(s, req.StartPositionMs*10_000, time.Now(), lim, device)
	if err != nil {
		stop()
		problem.Send(w, problem.StreamLimitReached, "")
		return
	}
	for _, old := range replaced {
		pb.stopped(old, "taken-over")
	}
	// The browser asks this server only (rule 97).
	out := remoteView(rp, s)
	if len(titles) == 1 {
		out.Title = &titles[0]
		if ref.Kind == "episode" {
			if next, err := l.nextEpisode(ctx, p, ref); err == nil {
				out.Next = next
			}
		}
	}
	pb.Logger.Info("playback started on a paired server", "playback", s.ID, "peer", peer, "method", out.Method)
	s.View = out
	pb.Tickets.Describe(s, describe(out.Title))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeOK(w, out)
}

// remoteView is a playback on a paired server as this server's site
// shows it: every address this server's own, under the ticket of its
// playback here (s); the paired server's are never seen. With msfp-go's
// revised v0 the copy's tracks, segments and previews come from the
// catalog's copy, not the play's answer: plan phase 7 adds them
// (federation is off on this branch); until then only the stream.
func remoteView(rp msfp.RemotePlayback, s *playback.Stream) sessionJSON {
	base := "/api/v1/stream/" + s.Ticket + "/"
	out := sessionJSON{ID: s.ID, Method: rp.Method, PositionMs: rp.PositionMs, DurationMs: rp.DurationMs,
		VersionID: rp.VersionID, AudioTrack: rp.AudioTrack, MaxBitrateKbps: rp.MaxBitrateKbps}
	if rp.Protocol == "hls" {
		out.Stream = streamJSON{URL: base + "master.m3u8", Protocol: "hls", MimeType: "application/vnd.apple.mpegurl"}
	} else {
		out.Stream = streamJSON{URL: base + "file", Protocol: "file", MimeType: rp.MimeType}
	}
	return out
}

// RemoteEnded ends this server's side of a playback on a paired server
// that ended there, or with the pairing (msfp.RemotePlaybackEnded): the
// viewer's player stops.
func (pb Playback) RemoteEnded(id string) {
	for _, s := range pb.Tickets.Where(func(s *playback.Stream) bool { return s.Remote != nil && s.Remote.ID == id }) {
		s.Remote.Replaced.Store(true) // over there already
		pb.end(s, "pairing ended", "")
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
