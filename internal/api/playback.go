package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/msfp-io/msfp-go"
	"github.com/overfin/server/internal/delivery"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/lang"
	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
)

// Playback starts, follows, ends and restores playbacks.
type Playback struct {
	Library        Library // for the active profile and Jellyfin access
	Tickets        *playback.Tickets
	MaxBitrateKbps int // the operator's cap; 0 means none
	// MaxTranscodes in total; 0 means no limit. Streams per account come
	// from the account's template.
	MaxTranscodes int
	// RefuseOverLimit refuses a stream over the account's limit instead of
	// replacing its oldest.
	RefuseOverLimit bool
	// PeerTranscodeReserve is how many transcodes paired servers can never
	// take; Engine, federation now, the MSFP engine (nil without it).
	PeerTranscodeReserve int
	Engine               func() *msfp.Engine
	// Delivery carries out plays of the copy index's copies (package
	// delivery): the file from disk, or a remux or a transcode by the
	// companion plugin. nil without it.
	Delivery *delivery.Executor
	Logger   *slog.Logger
}

func (Playback) Name() string { return "playback" }

func (pb Playback) Register(r *plugin.Registrar) error {
	for pattern, h := range map[string]http.HandlerFunc{
		"POST /api/v1/playback":               pb.start,
		"POST /api/v1/previews":               pb.startPreviews,
		"GET /api/v1/playback/current":        pb.current,
		"GET /api/v1/playback/{id}":           pb.get,
		"POST /api/v1/playback/{id}/progress": pb.progress,
		"POST /api/v1/playback/{id}/stop":     pb.stop,
		"PUT /api/v1/me/subtitles":            pb.setSubtitles,
		"PUT /api/v1/playback/{id}/tracks":    pb.chooseTracks,
	} {
		if err := r.HandleFunc(pattern, h); err != nil {
			return err
		}
	}
	return nil
}

// IdleTimeout ends a playback with neither progress reports nor stream
// requests for this long. The site's player reports while playing and when
// paused; a longer pause frees the playback, and the player starts it
// again when it's resumed.
const IdleTimeout = 2 * time.Minute

// RunJanitor ends idle playbacks, and their transcodes, until ctx is done.
// Jellyfin doesn't stop abandoned transcodes by itself (rule 50). current
// is the Playback in use now (settings reloads make a new one), whole: an
// abandoned playback's position is kept like any other's.
func RunJanitor(ctx context.Context, current func() Playback) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			pb := current()
			pb.EndIdle(now)
			pb.PingDeliveries(now)
		}
	}
}

// EndIdle ends the playbacks idle at now.
func (pb Playback) EndIdle(now time.Time) {
	for _, s := range pb.Tickets.Idle(now, IdleTimeout) {
		pb.end(s, "expired", "")
	}
}

// PingDeliveries keeps the plugin's deliveries of live playbacks alive
// (its watchdog stops one after two minutes without a segment read or a
// ping: a paused player reads nothing); a delivery gone there ends its
// playback here.
func (pb Playback) PingDeliveries(now time.Time) {
	if pb.Delivery == nil {
		return
	}
	for _, s := range pb.Tickets.Where(func(s *playback.Stream) bool { return s.Delivery != nil && s.Delivery.ID != "" }) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			err := pb.Delivery.Ping(ctx, s.Delivery)
			switch {
			case errors.Is(err, delivery.ErrGone):
				pb.Logger.Info("a playback's delivery ended at the plugin: the playback ends", "playback", s.ID)
				pb.end(s, "expired", "")
			case err != nil:
				pb.Logger.Warn("could not ping a playback's delivery", "playback", s.ID, "err", err)
			}
		}()
	}
}

// end stops a playback once: it is forgotten at once, and Jellyfin is told
// in the background, its transcode included.
func (pb Playback) end(s *playback.Stream, reason, by string) {
	if pb.Tickets.Remove(s, reason, by, time.Now()) {
		pb.stopped(s, reason)
	}
}

// stopped tells Jellyfin a playback, just removed for reason (as
// Tickets.Remove has it), has ended: where the profile stopped, and its
// transcode stopped.
func (pb Playback) stopped(s *playback.Stream, reason string) {
	snap := pb.Tickets.Snapshot(s)
	if rm := s.Remote; rm != nil {
		// Where the profile is, kept here; the playback ends there (the
		// engine sends stop, again when it doesn't answer), unless it is
		// over there already, or the engine stops it itself before the
		// playback replacing it (Replaced).
		eng := pb.engine()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			pb.record(ctx, s, snap.PositionTicks/10_000, true)
			if eng != nil && !rm.Replaced.Load() {
				eng.Stop(ctx, rm.ID(), snap.PositionTicks/10_000)
			}
		}()
		return
	}
	if peerTicket(s) {
		// A paired server's playback here, ended on this side (idle, the
		// operator, a playback taking its place): the engine ends its
		// side, its ticket refused from then on, and tells why.
		if eng := pb.engine(); eng != nil {
			for _, v := range eng.Playbacks() {
				if v.Handle == s.ID {
					eng.StopPlayback(v.ID, hostEnding(reason))
				}
			}
		}
	}
	if s.Delivery != nil {
		// The executor's: no Jellyfin session (rule 5); the plugin's
		// delivery, if any, is stopped (a file has nothing to stop).
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			pb.record(ctx, s, snap.PositionTicks/10_000, true)
			if pb.Delivery != nil {
				if err := pb.Delivery.Stop(ctx, s.Delivery); err != nil {
					pb.Logger.Warn("could not stop a playback's delivery", "playback", s.ID, "err", err)
				}
			}
		}()
		return
	}
}

// hostEnding is how a paired server's playback ended here, by this
// server's rules, in MSFP's words (playback section 6.3): idle is
// expiry; anything else, the operator's doing.
func hostEnding(reason string) msfp.EndReason {
	if reason == "expired" {
		return msfp.EndExpired
	}
	return msfp.EndOperator
}

// EndByAdmin ends a playback from the control panel's live view; its
// player is told an administrator stopped it. False when it had already
// ended.
// EndWhere stops the playbacks match keeps: those of a session that
// ended, or of a profile whose access changed (it starts again under the
// new limits, or not at all).
func (pb Playback) EndWhere(match func(*playback.Stream) bool) {
	for _, s := range pb.Tickets.Where(match) {
		pb.end(s, "stopped", "")
	}
}

// EndOf stops an account's playbacks, or only one session's of it; never a
// paired server's viewers'.
func (pb Playback) EndOf(accountID, sessionID string) {
	pb.EndWhere(func(s *playback.Stream) bool {
		return s.AccountID == accountID && (sessionID == "" || s.SessionID == sessionID)
	})
}

func (pb Playback) EndByAdmin(id string) bool {
	s, ok := pb.Tickets.Find(id, time.Now())
	if !ok || !pb.Tickets.Remove(s, "ended-by-admin", "", time.Now()) {
		return false
	}
	pb.stopped(s, "ended-by-admin")
	return true
}

// describe puts what plays in words for the live view: a film's name and
// year, or an episode's series, number and name.
func describe(t *library.TitleSummary) string {
	if t == nil {
		return ""
	}
	name := t.Name
	if t.Kind == "episode" && t.Series != nil {
		num := ""
		if t.SeasonNumber != nil && t.EpisodeNumber != nil {
			num = fmt.Sprintf("S%d E%d · ", *t.SeasonNumber, *t.EpisodeNumber)
		}
		return t.Series.Name + " · " + num + name
	}
	if t.Year > 0 {
		name += fmt.Sprintf(" (%d)", t.Year)
	}
	return name
}

// ---- Progress, stop, restore ---------------------------------------------------

type progressRequest struct {
	PositionMs int64 `json:"positionMs"`
	Paused     bool  `json:"paused"`
}

var playIDRe = regexp.MustCompile(`^play_[a-z2-7]{26}$`)

// find returns the caller's playback, or writes the right answer: 404, or
// 410 with the reason it ended.
func (pb Playback) find(w http.ResponseWriter, r *http.Request) *playback.Stream {
	id := r.PathValue("id")
	if !playIDRe.MatchString(id) {
		problem.Send(w, problem.NotFound, "")
		return nil
	}
	p := pb.Library.active(w, r)
	if p == nil {
		return nil
	}
	s, ended, ok := pb.Tickets.ByID(id, p.ID, time.Now())
	switch {
	case !ok:
		problem.Send(w, problem.NotFound, "")
	case ended != nil:
		sendEnded(w, ended)
	default:
		return s
	}
	return nil
}

func sendEnded(w http.ResponseWriter, e *playback.Ended) {
	switch e.Reason {
	case "taken-over":
		prob := problem.New(problem.PlaybackTakenOver, "")
		prob.TakenOverBy = e.By
		problem.Write(w, prob)
	case "expired":
		problem.Send(w, problem.PlaybackExpired, "")
	case "ended-by-admin":
		problem.Send(w, problem.PlaybackEndedByAdmin, "")
	default:
		problem.Send(w, problem.PlaybackStopped, "")
	}
}

func (pb Playback) progress(w http.ResponseWriter, r *http.Request) {
	var req progressRequest
	if !decode(r, &req) || req.PositionMs < 0 || req.PositionMs > maxPositionMs {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	s := pb.find(w, r)
	if s == nil {
		return
	}
	pb.Tickets.Progress(s, req.PositionMs*10_000, req.Paused, time.Now())
	pb.record(r.Context(), s, req.PositionMs, false)
	if rm := s.Remote; rm != nil {
		// Played on a paired server: its stream's use keeps it alive
		// there; the engine tells it where the viewer is, now and then,
		// and pauses at once.
		if eng := pb.engine(); eng != nil {
			eng.Progress(rm.ID(), req.PositionMs, req.Paused)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// What the executor's delivery makes was checked when it started; no
	// Jellyfin session to report to (rule 5).
	w.WriteHeader(http.StatusNoContent)
}

// stop is safe to call more than once: a playback already ended, or unknown,
// is simply already stopped.
func (pb Playback) stop(w http.ResponseWriter, r *http.Request) {
	var req progressRequest
	if !decode(r, &req) || req.PositionMs < 0 || req.PositionMs > maxPositionMs || !playIDRe.MatchString(r.PathValue("id")) {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	p := pb.Library.active(w, r)
	if p == nil {
		return
	}
	if s, ended, ok := pb.Tickets.ByID(r.PathValue("id"), p.ID, time.Now()); ok && ended == nil {
		// Watched by where it stopped alone: past the copy's credits, or
		// most of it (D9).
		pb.Tickets.Progress(s, req.PositionMs*10_000, false, time.Now())
		pb.end(s, "stopped", "")
	}
	w.WriteHeader(http.StatusNoContent)
}

func (pb Playback) get(w http.ResponseWriter, r *http.Request) {
	if s := pb.find(w, r); s != nil {
		pb.writeView(w, s)
	}
}

// current restores this session's latest playback by the active profile, at
// its last reported position, after a reload. Another profile's playback on
// the same device stays private (rule 73).
func (pb Playback) current(w http.ResponseWriter, r *http.Request) {
	c := pb.Library.Sessions.Current(w, r)
	if c == nil {
		return
	}
	s, ok := pb.Tickets.Latest(c.Session.ID, c.Profile.ID, time.Now())
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	pb.writeView(w, s)
}

func (pb Playback) writeView(w http.ResponseWriter, s *playback.Stream) {
	position := pb.Tickets.Snapshot(s).PositionTicks / 10_000
	switch view := s.View.(type) {
	case playJSON:
		view.PositionMs = position
		writeOK(w, view)
	default:
		problem.Send(w, problem.Internal, "")
	}
}

var (
	// A track ID: a or s, and a number from 0 to 999 without leading
	// zeros (MSFP playback section 2.1).
	trackID  = regexp.MustCompile(`^[as](0|[1-9][0-9]{0,2})$`)
	deviceRe = regexp.MustCompile(`^[\x20-\x7e]{1,64}$`)
)

// trackIndex turns "a3" or "s5" (with the expected letter) into 3 or 5.
func trackIndex(id string, letter byte) (int, bool) {
	m := trackID.FindStringSubmatch(id)
	if m == nil || id[0] != letter {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	return n, true
}

// sheetCount is how many sheets a set of previews takes.
func sheetCount(tp jellyfin.Trickplay) int {
	per := tp.Columns * tp.Rows
	if per <= 0 || tp.Count <= 0 {
		return 0
	}
	return (tp.Count + per - 1) / per
}

var languageRe = regexp.MustCompile(`^[a-zA-Z]{2,3}(-[a-zA-Z0-9]{2,8})*$`)

// languageTag keeps well-formed language codes only.
func languageTag(s string) string {
	if len(s) > 35 || !languageRe.MatchString(s) {
		return ""
	}
	return s
}

// Bounds on what a browser says of a playback: positions within a hundred
// days (ticks are milliseconds × 10,000 in an int64), bitrates within
// 10 Gb/s, so no multiplication overflows.
const (
	maxPositionMs = 100 * 24 * 3600 * 1000
	maxKbps       = 10_000_000
)

// sheetNumber reads a sheet's number, in decimal without leading zeros,
// within tp's sheets.
func sheetNumber(tp *jellyfin.Trickplay, sheet string) (int, bool) {
	n, err := strconv.Atoi(sheet)
	if err != nil || tp == nil || n < 0 || n >= sheetCount(*tp) || strconv.Itoa(n) != sheet {
		return 0, false
	}
	return n, true
}

// serveSheet serves sheet of a playback of this server's copy, its
// access checked by the caller: Overfin's own previews only (none from
// Jellyfin, plan decision 4).
func (pb Playback) serveSheet(w http.ResponseWriter, r *http.Request, s *playback.Stream, sheet string) {
	pb.serveOwnSheet(w, r, s.Previews, s.Trickplay, sheet)
}

// serveOwnSheet serves sheet of the previews Overfin made, kept by the
// fingerprint fp, of the layout tp: a previews ticket's.
func (pb Playback) serveOwnSheet(w http.ResponseWriter, r *http.Request, fp string, tp *jellyfin.Trickplay, sheet string) {
	n, ok := sheetNumber(tp, sheet)
	if !ok || fp == "" || pb.Library.Previews == nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	l := pb.Library
	data, err := l.Images.Fetched(r.Context(), fmt.Sprintf("previews/%s/%d", fp, n),
		func(context.Context) ([]byte, error) { return l.Previews.Store.Sheet(fp, n) })
	if err != nil {
		if r.Context().Err() == nil {
			pb.Logger.Warn("a seek-bar preview sheet failed", "err", err)
		}
		problem.Send(w, problem.NotFound, "")
		return
	}
	writeSheet(w, data)
}

func writeSheet(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}

// setSubtitles sets the subtitles the active profile prefers: a language
// ("" for none), its next playbacks start with, when the video has it and
// nothing else was chosen for the title.
func (pb Playback) setSubtitles(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Language string `json:"language"`
	}
	if !decode(r, &req) || (req.Language != "" && lang.Tag(req.Language) == "") {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	l := pb.Library
	p := l.active(w, r)
	if p == nil {
		return
	}
	if err := l.Provisioner.Store.SetSubtitleLanguage(r.Context(), p.AccountID, p.ID, lang.Tag(req.Language)); err != nil {
		l.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// wishesFor is trackWishes by the title's key (a film's or a series'; ""
// when unknown).
func (l Library) wishesFor(ctx context.Context, p *store.Profile, key, siteLanguage string) (audio, subtitles string) {
	audio, subtitles = p.AudioLanguage, p.SubtitleLanguage
	st := l.Provisioner.Store
	if key != "" {
		chosen, err := st.TitleTracks(ctx, p.ID, key)
		if err != nil {
			l.Logger.Warn("reading a title's tracks chosen failed", "err", err)
		}
		if chosen.Audio != "" {
			audio = chosen.Audio
		}
		if chosen.Subtitles != nil {
			subtitles = *chosen.Subtitles
		}
	}
	switch audio {
	case "":
		audio = siteLanguage
	case "original":
		audio = ""
		if key != "" {
			if infos, err := st.TitleInfos(ctx, []string{key}, today(), ""); err == nil && infos[key] != nil {
				audio = infos[key].Details.Language
			}
		}
	}
	return audio, subtitles
}

// trackKey is the title whose tracks a playback of an item chooses: a
// film's, an item's, an episode's series'; "" when the item has none yet.
func (l Library) trackKey(ctx context.Context, item string) (string, error) {
	keys, err := l.Provisioner.Store.TitleKeys(ctx, []string{item})
	if err != nil {
		return "", err
	}
	ref, ok := store.ParseTitleKey(keys[item])
	if !ok {
		return "", nil
	}
	if ref.Kind == "series" {
		ref = ref.Series()
	}
	return ref.Key(), nil
}

// chooseTracks keeps the audio or the subtitles chosen while watching, by
// their language, for the playback's title: its next playbacks start with
// them, on any device, over the profile's preferences. subtitleTrack ""
// is none. A track without a known language changes nothing kept.
func (pb Playback) chooseTracks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AudioTrack    *string `json:"audioTrack"`
		SubtitleTrack *string `json:"subtitleTrack"`
	}
	if !decode(r, &req) || (req.AudioTrack == nil && req.SubtitleTrack == nil) {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	if req.AudioTrack != nil {
		if _, ok := trackIndex(*req.AudioTrack, 'a'); !ok {
			problem.Send(w, problem.InvalidRequest, "")
			return
		}
	}
	if req.SubtitleTrack != nil && *req.SubtitleTrack != "" {
		if _, ok := trackIndex(*req.SubtitleTrack, 's'); !ok {
			problem.Send(w, problem.InvalidRequest, "")
			return
		}
	}
	s := pb.find(w, r)
	if s == nil {
		return
	}
	ctx := r.Context()
	l := pb.Library
	if s.Remote != nil {
		pb.chooseRemoteTracks(w, r, s, req.AudioTrack, req.SubtitleTrack)
		return
	}
	key, err := l.trackKey(ctx, s.ItemID)
	if err != nil {
		l.storeError(w, err)
		return
	}
	if key == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if d := s.Delivery; d != nil {
		// The copy's tracks, by their IDs.
		if err := pb.keepDeliveryTracks(ctx, s, d, key, req.AudioTrack, req.SubtitleTrack); err != nil {
			l.storeError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// keepDeliveryTracks keeps, for a delivery's playback, the language of
// the audio (the film's own sound only) or of the subtitles chosen (none
// for ""; a forced track changes nothing), by the copy's track IDs.
func (pb Playback) keepDeliveryTracks(ctx context.Context, s *playback.Stream, d *delivery.Started, key string, audio, subtitle *string) error {
	st := pb.Library.Provisioner.Store
	if audio != nil {
		for _, t := range d.Plan.Copy.Facts.AudioTracks {
			if t.ID == *audio && t.Language != "" && t.Role == "" {
				if err := st.SetTitleAudio(ctx, s.ProfileID, key, t.Language); err != nil {
					return err
				}
			}
		}
	}
	switch {
	case subtitle == nil:
	case *subtitle == "":
		return st.SetTitleSubtitles(ctx, s.ProfileID, key, "")
	default:
		if t, ok := d.Subtitle(*subtitle); ok && t.Language != "" && !t.Forced {
			return st.SetTitleSubtitles(ctx, s.ProfileID, key, t.Language)
		}
	}
	return nil
}
