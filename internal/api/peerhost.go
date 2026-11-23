package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/msfp-io/msfp-go"
	"github.com/msfp-io/msfp-go/wire"
	"github.com/overfin/server/internal/delivery"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/store"
)

// A paired server's viewer plays one of our titles (ROADMAP.md milestone
// 6): the MSFP engine hears the paired server's play, checks the pairing,
// finds the copy among the title's (the catalog gives each film and
// episode its copies from the copy index, store.MSFPCatalog), picks the
// audio, decides what is served (wire.Serve), counts the pairing's
// limits, and asks this server to describe the copy and start it
// (PeerHost, msfp.Playback). This server plays it through the same
// executor as its own people's (package delivery): the file from disk,
// or a remux or a transcode by the companion plugin, within the server's
// transcodes less those kept for its own people. Its media go through the
// engine's checks to Serve (msfp.Media), by the playback's ID here (its
// handle), never by a ticket of ours. Everything personal (what they
// watch, where they are, their address) stays on their server.

// PeerHost is this server's playback and media for the engine: what the
// paired servers' viewers play here. Current is the Playback in use now
// (settings reloads make a new one); MayBurn is the operator's burn-in
// switch for paired servers (off by default: rule 17).
type PeerHost struct {
	Current func() (Playback, bool)
	MayBurn func() bool
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

// copyOf is the copy index's copy a handle names (store.PeerHandle: the
// copy ID and the library of the place the catalog gave it through).
func (pb Playback) copyOf(ctx context.Context, handle string) (store.IndexedCopy, error) {
	c, ok, err := pb.Library.Provisioner.Store.CopyByHandle(ctx, handle)
	switch {
	case err != nil:
		return store.IndexedCopy{}, msfp.ErrServerBusy
	case !ok:
		return store.IndexedCopy{}, msfp.ErrNotFound
	}
	return c, nil
}

// notApart are the subtitle formats never served apart (rule 17): DVD
// and DVB pictures.
var notApart = []string{"dvd_subtitle", "dvb_subtitle"}

// Describe gives what the catalog doesn't say of a copy (msfp.CopyInfo):
// no fonts (ASS is served as its text only, this release), and the
// subtitle tracks not served apart: DVD and DVB pictures, and a track
// Jellyfin's numbering doesn't map. Jellyfin isn't asked.
func (h PeerHost) Describe(ctx context.Context, copy string) (msfp.CopyInfo, error) {
	pb, err := h.playback()
	if err != nil {
		return msfp.CopyInfo{}, err
	}
	c, err := pb.copyOf(ctx, copy)
	if err != nil {
		return msfp.CopyInfo{}, err
	}
	var info msfp.CopyInfo
	for _, t := range c.Facts.SubtitleTracks {
		if _, mapped := c.Streams[t.ID]; !mapped || slices.Contains(notApart, t.Format) {
			info.NotApart = append(info.NotApart, t.ID)
		}
	}
	return info, nil
}

// Start plays a copy for a paired server's viewer through the executor,
// as the engine decided (req.Delivery, req.MediaType, req.Transcode, the
// audio track), for its purpose (a download or a scan only within the
// pairing's limits: req.Admit counts them), admitted by the pairing's
// limits (req.Admit, the engine's count) and then by this server's
// transcodes, less those kept for its own people (PeerTranscodeReserve).
// A burned subtitle only with the operator's switch.
func (h PeerHost) Start(ctx context.Context, req msfp.StartRequest) (msfp.Started, error) {
	pb, err := h.playback()
	if err != nil {
		return msfp.Started{}, err
	}
	if req.AudioOnly || req.Parts > 1 {
		// Overfin shares no music; a copy of several files isn't offered.
		return msfp.Started{}, msfp.ErrNotPlayable
	}
	c, err := pb.copyOf(ctx, req.Copy)
	if err != nil {
		return msfp.Started{}, err
	}
	play := wire.PlayRequest{Title: req.Title, Purpose: req.Purpose, Delivery: req.Delivery, MediaType: req.MediaType,
		Transcode: req.Transcode, AudioRenditions: req.AudioRenditions, StartPositionMs: req.StartMs,
		MaxBitrateKbps: req.MaxBitrateKbps, AudioTrack: req.AudioTrack, VersionID: c.CopyID}
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
	ja, _, err := pb.Library.jf(ctx)
	if err != nil {
		return msfp.Started{}, msfp.ErrServerBusy
	}
	a := deliveryArgs{lim: lim, device: req.Peer.Name, accountID: id, profileID: id, sessionID: id, auth: ja, admit: req.Admit}
	if req.Purpose != wire.PurposeDownload && req.Purpose != wire.PurposeScan {
		a.trickplay, a.previews = pb.ownPreviews(ctx, c)
	}
	a.creditsMs = pb.credits(ctx, c)
	mayBurn := h.MayBurn != nil && h.MayBurn()
	s, err := pb.startDelivery(ctx, c, delivery.Request{Play: play, CapKbps: req.MaxBitrateKbps, MayBurn: mayBurn}, a)
	if err != nil {
		return msfp.Started{}, pb.peerStartError(err)
	}
	d := s.Delivery
	pb.Logger.Info("playback started for a paired server", "playback", s.ID, "peer", req.Peer.Name, "purpose", req.Purpose,
		"delivery", d.Plan.Kind, "method", d.Method())
	// What plays, for the live view: the title (ours), the server playing
	// it showing as the account (the control panel).
	pb.Tickets.Describe(s, pb.Library.Provisioner.Store.ItemDescription(ctx, c.ItemID))
	return msfp.Started{Handle: s.ID, Method: d.Method(), Protocol: d.Protocol, PositionMs: d.Plan.StartMs}, nil
}

// peerStartError is a start's failure as the engine's error.
func (pb Playback) peerStartError(err error) error {
	var me *msfp.Error
	switch {
	case errors.As(err, &me):
		return err // the pairing's own limits (req.Admit)
	case errors.Is(err, playback.ErrStreamLimit):
		return msfp.ErrStreamLimitReached
	case errors.Is(err, playback.ErrServerBusy):
		return msfp.ErrServerBusy
	case errors.Is(err, errNotPlayable):
		return msfp.ErrNotPlayable
	}
	if rf := delivery.AsRefusal(err); rf != nil {
		pb.Logger.Info("a paired server's play refused", "status", rf.Status, "why", rf.Why)
		if rf.Status == http.StatusServiceUnavailable {
			return msfp.ErrServerBusy
		}
		return msfp.ErrDeliveryRefused
	}
	return msfp.ErrServerBusy
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
// isn't idle (IdleTimeout), and the live view shows where it stands. A
// report without a position keeps the one it had; a download's late
// report (its playback over) changes nothing here: what they watch is
// kept on their server.
func (h PeerHost) Progress(ctx context.Context, rep msfp.Report) error {
	pb, err := h.playback()
	if err != nil {
		return err
	}
	if rep.Late {
		return nil
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
// having checked its ticket and the request: the file's bytes for the
// engine's range, the executor's playlists and segments, subtitles in
// stretches, PGS, and previews' sheets (a playback's, or a copy's under a
// previews ticket: OfCopy), by the same code as this server's own, a
// text track whole too. ASS and fonts aren't served this release (D7).
func (h PeerHost) Serve(ctx context.Context, handle string, path msfp.MediaPath, rng *msfp.Range, out msfp.ResponseSink) error {
	pb, err := h.playback()
	if err != nil {
		return err
	}
	if path.OfCopy {
		return pb.serveCopySheet(ctx, handle, path, out)
	}
	s, ok := pb.peerStream(handle)
	if !ok || path.Entry != 0 {
		return msfp.ErrNotFound
	}
	switch path.Kind {
	case msfp.MediaASS, msfp.MediaFont:
		return msfp.ErrNotFound
	case msfp.MediaFile:
		return pb.servePeerFile(ctx, s, rng, out)
	}
	if path.Whole && path.Kind != msfp.MediaSubtitles {
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
	case msfp.MediaPlaylist, msfp.MediaSegment:
		r.SetPathValue("name", path.Name)
		st.serve(w, r)
	case msfp.MediaSubtitles, msfp.MediaPictures:
		r.SetPathValue("track", path.Track)
		r.SetPathValue("part", strconv.Itoa(path.Part))
		if path.Whole {
			r.SetPathValue("part", "whole")
		}
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

// servePeerFile writes a file delivery's bytes for the engine's range (the
// whole file for none): the engine writes the status and the headers,
// and counts what is sent (a scan's cap, a download's pace).
func (pb Playback) servePeerFile(ctx context.Context, s *playback.Stream, rng *msfp.Range, out msfp.ResponseSink) error {
	d := s.Delivery
	if d == nil || d.Protocol != wire.ProtocolFile || pb.Delivery == nil {
		return msfp.ErrNotFound
	}
	f, err := pb.Delivery.OpenFile(ctx, d)
	if err != nil {
		return msfp.ErrNotFound
	}
	defer f.Close()
	first, n := int64(0), f.Info.Size()
	if rng != nil {
		first, n = rng.First, rng.Last-rng.First+1
	}
	if first < 0 || n < 0 || first+n > f.Info.Size() {
		return msfp.ErrRangeNotSatisfiable
	}
	if _, err := f.Seek(first, io.SeekStart); err != nil {
		return msfp.ErrNotFound
	}
	// While it flows, the playback is in use: a download is one long
	// read.
	pb.Tickets.Touch(s, time.Now())
	done := make(chan struct{})
	defer close(done)
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case now := <-tick.C:
				pb.Tickets.Touch(s, now)
			case <-done:
				return
			}
		}
	}()
	out.Start(http.StatusOK, d.Plan.Copy.Facts.MediaType, n, "")
	_, err = io.CopyN(out, f, n)
	return err
}

// serveCopySheet serves a sheet of a copy's previews under a previews
// ticket (MSFP video section 5.6: the handle is the copy's), from the
// previews Overfin made for its file.
func (pb Playback) serveCopySheet(ctx context.Context, handle string, path msfp.MediaPath, out msfp.ResponseSink) error {
	if path.Kind != msfp.MediaSheet || pb.Library.Previews == nil {
		return msfp.ErrNotFound
	}
	c, err := pb.copyOf(ctx, handle)
	if err != nil {
		return err
	}
	tp, fp := pb.ownPreviews(ctx, c)
	if tp == nil || path.Sheet < 0 || path.Sheet >= sheetCount(*tp) {
		return msfp.ErrNotFound
	}
	l := pb.Library
	data, err := l.Images.Fetched(ctx, fmt.Sprintf("previews/%s/%d", fp, path.Sheet),
		func(context.Context) ([]byte, error) { return l.Previews.Store.Sheet(fp, path.Sheet) })
	if err != nil {
		return msfp.ErrNotFound
	}
	out.Start(http.StatusOK, "image/jpeg", int64(len(data)), "")
	_, err = out.Write(data)
	return err
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
