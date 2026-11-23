package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/msfp-io/msfp-go"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/problem"
)

// A paired server's viewer plays one of our titles (ROADMAP.md milestone
// 6): the MSFP engine hears the paired server's play, checks the pairing,
// finds our copies in the libraries offered to it, picks the copy and
// the tracks, counts the pairing's limits, and asks this server to
// describe the copy and start it (PeerHost, msfp.Playback). This server
// plays it as it plays for its own people, Overfin's one Jellyfin account
// playing as a device of the playback's own, within the server's
// transcodes less those kept for its own people. Its media go through
// the engine's checks to Serve (msfp.Media), by the playback's ID here
// (its handle), never by a ticket of ours. Everything personal (what
// they watch, where they are, their address) stays on their server.

// PeerHost is this server's playback and media for the engine: what the
// paired servers' viewers play here. Current is the Playback in use now
// (settings reloads make a new one).
type PeerHost struct {
	Current func() (Playback, bool)
}

var (
	_ msfp.Playback = PeerHost{}
	_ msfp.Media    = PeerHost{}
)

// errNoPlayback is a host not built yet: busy for now.
var errNoPlayback = msfp.ErrServerBusy

func (h PeerHost) playback() (Playback, error) {
	pb, ok := h.Current()
	if !ok || pb.Tickets == nil {
		return Playback{}, errNoPlayback
	}
	return pb, nil
}

// Describe gives what the catalog doesn't say of a copy (msfp.CopyInfo:
// fonts, tracks not served apart, forms): with msfp-go's revised v0 a
// copy's tracks, length and previews' layout travel on the title
// (wire.Copy), which this server doesn't fill yet. Federation is off on
// this branch until plan phase 7, which describes copies from the copy
// index; until then this checks only that Jellyfin has the copy.
func (h PeerHost) Describe(ctx context.Context, copy string) (msfp.CopyInfo, error) {
	pb, err := h.playback()
	if err != nil {
		return msfp.CopyInfo{}, err
	}
	l := pb.Library
	ja, uid, err := l.jf(ctx)
	if err != nil {
		return msfp.CopyInfo{}, msfp.ErrServerBusy
	}
	play := ja
	play.DeviceID = pb.Tickets.ReserveDevice("overfin-play")
	defer pb.Tickets.ReleaseDevice(play.DeviceID)
	var pr jellyfin.PlaybackRequest
	pr.DeviceProfile = playback.DeviceProfile(playback.Capabilities{}, 0)
	info, err := l.Jellyfin.PlaybackInfo(ctx, play, uid, copy, pr)
	switch {
	case jellyfin.IsStatus(err, http.StatusNotFound):
		return msfp.CopyInfo{}, msfp.ErrNotFound
	case err != nil:
		return msfp.CopyInfo{}, msfp.ErrServerBusy
	case len(info.MediaSources) == 0:
		return msfp.CopyInfo{}, msfp.ErrNotPlayable
	}
	return msfp.CopyInfo{}, nil
}

// Start plays a copy for a paired server's viewer: Jellyfin is asked how
// (by the viewer's device's capabilities, as the engine passes them),
// the tracks the engine chose are applied, and the playback is admitted
// by the pairing's limits (req.Admit, the engine's count) and then by
// this server's transcodes, less those kept for its own people
// (PeerTranscodeReserve). A remux that Progress later finds re-encoding
// its video is moved into the pairing's transcodes by the slot Admit
// gave (msfp.Slot.Transcoding), and stopped when none is left.
func (h PeerHost) Start(ctx context.Context, req msfp.StartRequest) (msfp.Started, error) {
	pb, err := h.playback()
	if err != nil {
		return msfp.Started{}, err
	}
	if req.AudioOnly {
		// Overfin shares no music.
		return msfp.Started{}, msfp.ErrNotPlayable
	}
	if req.Parts > 1 {
		// A copy of several files must come whole, as HLS (MSFP media
		// section 1); Jellyfin plays its parts one by one, as separate
		// streams, so this server can't serve it as one yet.
		return msfp.Started{}, msfp.ErrNotPlayable
	}
	l := pb.Library
	ja, uid, err := l.jf(ctx)
	if err != nil {
		return msfp.Started{}, msfp.ErrServerBusy
	}
	// msfp-go's revised v0 sends no device capabilities: the engine
	// decides what is served (req.Delivery, req.Served). Until plan
	// phase 7 runs it through the executor, Jellyfin is asked as for a
	// device of no stated capabilities (federation is off on this
	// branch: no engine calls this).
	var caps playback.Capabilities
	kbps := req.MaxBitrateKbps
	var pr jellyfin.PlaybackRequest
	pr.StartTimeTicks = req.StartMs * 10_000
	pr.MaxStreamingBitrate = kbps * 1000
	pr.DeviceProfile = playback.DeviceProfile(caps.Exact(), pr.MaxStreamingBitrate)
	off := -1
	pr.SubtitleStreamIndex = &off
	// The server's transcodes less those kept for our own people; the
	// pairing's streams and transcodes are the engine's (req.Admit).
	var lim playback.Limits
	if pb.MaxTranscodes > 0 {
		lim.MaxTranscodes = pb.MaxTranscodes - pb.PeerTranscodeReserve
		if lim.MaxTranscodes <= 0 {
			none := 0
			lim.AccountTranscodes, lim.MaxTranscodes = &none, 0
		}
	}
	lim.RefuseOverLimit = true
	id := playback.PeerPrefix + req.Peer.ID
	w := &recorder{header: http.Header{}}
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
	o, ok := pb.open(w, r, openArgs{req: startRequest{AudioTrack: req.AudioTrack,
		StartPositionMs: req.StartMs}, pr: pr, kbps: kbps, item: req.Copy, uid: uid, ja: ja, lim: lim, device: req.Peer.Name,
		accountID: id, profileID: id, sessionID: id, peerTracks: true, admit: req.Admit})
	if !ok {
		if w.err != nil {
			return msfp.Started{}, w.err
		}
		return msfp.Started{}, w.refusal()
	}
	s, out := o.stream, o.out
	// Its previews, by the same set Describe gave.
	pb.previews(ctx, s, ja, uid, func(int) string { return "" })
	pb.Logger.Info("playback started for a paired server", "playback", s.ID, "peer", req.Peer.Name, "method", o.dec.Method,
		"reasons", o.dec.Reasons(), "maxKbps", kbps)
	s.View = out
	// What plays, for the live view: the title (ours), the server playing
	// it showing as the account (the control panel).
	pb.Tickets.Describe(s, l.Provisioner.Store.ItemDescription(ctx, req.Copy))
	if err := l.Jellyfin.ReportStart(ctx, s.Auth, pb.state(s, pb.Tickets.Snapshot(s))); err != nil {
		pb.Logger.Warn("could not report a playback start", "playback", s.ID, "err", err)
	}
	// msfp-go's revised v0 takes no media type, information or
	// segments here: the copy's facts are the catalog's (plan phase 7).
	return msfp.Started{Handle: s.ID, Method: o.dec.Method, Protocol: o.dec.Protocol, PositionMs: out.PositionMs}, nil
}

// peerStream is a paired server's playback here, by its ID.
func (pb Playback) peerStream(handle string) (*playback.Stream, bool) {
	for _, s := range pb.Tickets.Where(func(s *playback.Stream) bool { return s.ID == handle }) {
		if strings.HasPrefix(s.SessionID, playback.PeerPrefix) {
			return s, true
		}
	}
	return nil, false
}

// Progress is a paired server's viewer still watching: the playback here
// isn't idle (IdleTimeout), and Jellyfin hears where it stands, as for
// this server's own. msfp-go's revised v0 reports no pause: a peer's
// playback shows as playing; a report without a position keeps the one
// it had. Downloads' late reports and the stop's own report are plan
// phase 7's (federation is off on this branch).
func (h PeerHost) Progress(ctx context.Context, rep msfp.Report) error {
	pb, err := h.playback()
	if err != nil {
		return err
	}
	s, ok := pb.peerStream(rep.Handle)
	if !ok {
		return msfp.ErrNotFound
	}
	ticks := pb.Tickets.Snapshot(s).PositionTicks
	if rep.HasPosition {
		ticks = rep.PositionMs * 10_000
	}
	pb.Tickets.Progress(s, ticks, false, time.Now())
	// Whether a remux really copies the video, as for our own people's;
	// one that re-encodes counts in the pairing's transcodes (Start).
	if pb.Tickets.Method(s) == "remux" {
		s.Checked.Do(func() { go pb.checkRemux(s) })
	}
	if err := pb.Library.Jellyfin.ReportProgress(ctx, s.Auth, pb.state(s, pb.Tickets.Snapshot(s))); err != nil {
		pb.Logger.Warn("could not report progress", "playback", s.ID, "err", err)
	}
	return nil
}

// endReasons are the engine's reasons as the live view words them.
var endReasons = map[msfp.EndReason]string{
	msfp.EndStopped:      "stopped",
	msfp.EndExpired:      "expired",
	msfp.EndTooLong:      "too long",
	msfp.EndPairingEnded: "pairing ended",
	msfp.EndNotOffered:   "no longer shared",
	msfp.EndOperator:     "stopped by the operator",
}

// Stop ends a paired server's playback here.
func (h PeerHost) Stop(ctx context.Context, handle string, reason msfp.EndReason) error {
	pb, err := h.playback()
	if err != nil {
		return err
	}
	if s, ok := pb.peerStream(handle); ok {
		pb.end(s, endReasons[reason], "")
	}
	return nil
}

// peerStreamKey carries, in a request's context, the paired server's
// playback the engine found by its ticket: the stream routes serve it
// without a ticket or a session of their own.
type peerStreamKey struct{}

// Serve serves the media of a paired server's playback here, the engine
// having checked its ticket and the request: Jellyfin's file, HLS
// rewritten to MSFP media 1's subset, subtitles in stretches, PGS, and
// previews' sheets, by the same code as this server's own.
func (h PeerHost) Serve(ctx context.Context, handle string, path msfp.MediaPath, rng *msfp.Range, out msfp.ResponseSink) error {
	pb, err := h.playback()
	if err != nil {
		return err
	}
	s, ok := pb.peerStream(handle)
	if !ok {
		return msfp.ErrNotFound
	}
	r, err := http.NewRequestWithContext(context.WithValue(ctx, peerStreamKey{}, s), http.MethodGet, "/", nil)
	if err != nil {
		return err
	}
	if rng != nil {
		r.Header.Set("Range", rangeHeader(*rng))
	}
	w := &sinkWriter{sink: out, header: http.Header{}}
	st := Stream{Playback: pb}
	switch path.Kind {
	case msfp.MediaMaster:
		r.SetPathValue("name", "master.m3u8")
		st.serve(w, r)
	case msfp.MediaFile:
		r.SetPathValue("name", "file")
		st.serve(w, r)
	case msfp.MediaPlaylist, msfp.MediaSegment:
		r.SetPathValue("name", path.Name)
		st.serve(w, r)
	case msfp.MediaSubtitles, msfp.MediaPictures:
		r.SetPathValue("track", path.Track)
		r.SetPathValue("part", strconv.Itoa(path.Part))
		if path.Kind == msfp.MediaPictures {
			st.pictures(w, r)
		} else {
			st.subtitles(w, r)
		}
	case msfp.MediaSheet:
		r.SetPathValue("sheet", strconv.Itoa(path.Sheet))
		st.trickplay(w, r)
	default:
		return msfp.ErrNotFound
	}
	return w.result()
}

// rangeHeader writes a range back as a Range header.
func rangeHeader(r msfp.Range) string {
	switch {
	case r.Suffix > 0:
		return "bytes=-" + strconv.FormatInt(r.Suffix, 10)
	case r.Last < 0:
		return "bytes=" + strconv.FormatInt(r.First, 10) + "-"
	}
	return "bytes=" + strconv.FormatInt(r.First, 10) + "-" + strconv.FormatInt(r.Last, 10)
}

// sinkWriter is an http.ResponseWriter onto the engine's ResponseSink: a
// success starts it with the headers MSFP needs; a failure (a status of
// 400 and up) starts nothing, and is given back as the engine's error,
// for the engine to answer as MSFP says.
type sinkWriter struct {
	sink    msfp.ResponseSink
	header  http.Header
	status  int
	started bool
}

func (w *sinkWriter) Header() http.Header { return w.header }

func (w *sinkWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	if status >= 400 {
		return
	}
	length := int64(-1)
	if v, err := strconv.ParseInt(w.header.Get("Content-Length"), 10, 64); err == nil {
		length = v
	}
	w.sink.Start(status, w.header.Get("Content-Type"), length, w.header.Get("Content-Range"))
	w.started = true
}

func (w *sinkWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if !w.started {
		return len(b), nil // a failure's body: never sent
	}
	return w.sink.Write(b)
}

// result is what the engine is told: nil once started; its errors for
// the statuses MSFP media has, else not found.
func (w *sinkWriter) result() error {
	switch {
	case w.started:
		return nil
	case w.status == http.StatusRequestedRangeNotSatisfiable:
		return msfp.ErrRangeNotSatisfiable
	case w.status == http.StatusServiceUnavailable, w.status == http.StatusTooManyRequests:
		return msfp.ErrServerBusy
	}
	return msfp.ErrNotFound
}

// recorder keeps what open answers when it fails, for Start to give the
// engine as its error.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
	err    error
}

func (w *recorder) Header() http.Header { return w.header }

func (w *recorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *recorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(b)
}

// refusal is open's failure as the engine's error.
func (w *recorder) refusal() error {
	var p struct {
		Type string `json:"type"`
	}
	json.Unmarshal(w.body.Bytes(), &p)
	t := problem.Type(p.Type)
	switch {
	case t == problem.NotPlayable:
		return msfp.ErrNotPlayable
	case t == problem.StreamLimitReached:
		return msfp.ErrStreamLimitReached
	case t == problem.ServerBusy, w.status >= 500:
		return msfp.ErrServerBusy
	case w.status == http.StatusNotFound:
		return msfp.ErrNotFound
	}
	return errors.New("api: a paired server's playback couldn't start")
}

// fileType is a file's media type by its container, as Jellyfin names it
// ("mkv", "mp4,m4v"…): "" when unknown, the engine then sending none.
//
//lint:ignore U1000 unused while federation is off on this branch; phase 7 serves peers again.
func fileType(container string) string {
	first, _, _ := strings.Cut(strings.ToLower(container), ",")
	switch first {
	case "mp4", "m4v", "mov":
		return "video/mp4"
	case "mkv", "matroska":
		return "video/x-matroska"
	case "webm":
		return "video/webm"
	case "ts", "mpegts":
		return "video/mp2t"
	}
	return ""
}
