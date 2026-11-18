package msfp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/overfin/server/msfp/wire"
)

// Media, on the owning side (MSFP media sections 1 to 4, video section
// 6): the stream routes of peers' tickets. Every request is checked
// before the host serves a byte: the ticket, live and of a pairing still
// paired; GET only; no query; no browser's headers; a Range header of
// one of the three forms, on the file and segments only; the fetches at
// once. The host's playlists are checked against media 1's subset before
// they leave.

// Bounds of the fetches at once on a peer's tickets (MSFP media section
// 2.4): per ticket and per peer, each with a queue as long, waited in
// for 10 seconds at most.
const (
	fetchesPerPlayback = 8
	fetchesPerPeer     = 64
	fetchWait          = 10 * time.Second
)

// streamRoute is where StreamHandler is mounted, after Config.Prefix.
const streamRoute = "/api/v1/stream/"

// StreamHandler serves the media of peers' playbacks here, under
// <Prefix>/api/v1/stream/: mount it there, on the raw path, with no
// authentication in front and no compression of its answers. A ticket
// the engine doesn't know (one the host gave its own people) goes to
// fallback, which may be nil: the host's not-found answer then
// (Config.NotFound).
func (e *Engine) StreamHandler(fallback http.Handler) http.Handler {
	if fallback == nil {
		fallback = e.cfg.NotFound
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.serveStream(w, r, fallback)
	})
}

// partRe is a subtitle part's or a sheet's number, without leading
// zeros.
var partRe = regexp.MustCompile(`^(0|[1-9][0-9]{0,3})$`)

// ParseMediaPath reads what a URL asks of a playback, after its ticket
// (or, at the host's own viewer route, after its own ID for the
// playback): "master.m3u8", "file", "subtitles/<track>/<part>",
// "pictures/<track>/<part>", "trickplay/<n>", or a bare name, a playlist
// when it ends in .m3u8 and a segment otherwise. Each part is checked as
// MSFP media section 1 says, compared as sent: nothing decoded; false
// for anything else. A host's viewer route serves playlists unchanged
// this way: their bare names resolve under it.
func ParseMediaPath(rest string) (MediaPath, bool) {
	parts := strings.Split(rest, "/")
	for _, s := range parts {
		if !wire.FileName(s) {
			return MediaPath{}, false
		}
	}
	number := func(s string) (int, bool) {
		n, err := strconv.Atoi(s)
		return n, err == nil && partRe.MatchString(s)
	}
	switch {
	case len(parts) == 1 && parts[0] == "master.m3u8":
		return MediaPath{Kind: MediaMaster}, true
	case len(parts) == 1 && parts[0] == "file":
		return MediaPath{Kind: MediaFile}, true
	case len(parts) == 1 && strings.HasSuffix(parts[0], ".m3u8"):
		return MediaPath{Kind: MediaPlaylist, Name: parts[0]}, true
	case len(parts) == 1:
		return MediaPath{Kind: MediaSegment, Name: parts[0]}, true
	case len(parts) == 2 && parts[0] == "trickplay":
		n, ok := number(parts[1])
		return MediaPath{Kind: MediaSheet, Sheet: n}, ok
	case len(parts) == 3 && (parts[0] == "subtitles" || parts[0] == "pictures"):
		n, ok := number(parts[2])
		kind := MediaSubtitles
		if parts[0] == "pictures" {
			kind = MediaPictures
		}
		return MediaPath{Kind: kind, Track: parts[1], Part: n}, ok
	}
	return MediaPath{}, false
}

// mediaPath reads what a request asks of a peer's playback here: false
// for anything the playback doesn't serve (another protocol's names, a
// track it hasn't, a sheet past the count).
func mediaPath(rest string, pb *playback) (MediaPath, bool) {
	p, ok := ParseMediaPath(rest)
	if !ok {
		return MediaPath{}, false
	}
	switch p.Kind {
	case MediaFile:
		return p, pb.protocol == "file"
	case MediaMaster, MediaPlaylist, MediaSegment:
		return p, pb.protocol == "hls"
	case MediaSheet:
		return p, p.Sheet < pb.sheets
	}
	pictures, has := pb.subtitles[p.Track]
	return p, has && pictures == (p.Kind == MediaPictures)
}

// ParseRange reads a Range header of MSFP media section 2.1: one range of
// bytes in one of three forms, each number 1 to 15 digits; never several
// ranges, "bytes=-" or "bytes=-0", nor a first byte past the last.
func ParseRange(h string) (Range, bool) {
	spec, ok := strings.CutPrefix(h, "bytes=")
	from, to, dash := strings.Cut(spec, "-")
	digits := func(s string) bool { return len(s) >= 1 && len(s) <= 15 && strings.Trim(s, "0123456789") == "" }
	if !ok || !dash || (from == "" && to == "") || from != "" && !digits(from) || to != "" && !digits(to) {
		return Range{}, false
	}
	a, _ := strconv.ParseInt(from, 10, 64)
	b, _ := strconv.ParseInt(to, 10, 64)
	switch {
	case from == "":
		return Range{Suffix: b}, b >= 1
	case to == "":
		return Range{First: a, Last: -1}, true
	}
	return Range{First: a, Last: b}, a <= b
}

// serveStream serves a request on the stream route.
func (e *Engine) serveStream(w http.ResponseWriter, r *http.Request, fallback http.Handler) {
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), e.cfg.Prefix+streamRoute)
	ticket, name, _ := strings.Cut(rest, "/")
	if !ok || !wire.ID(ticket) {
		fallback.ServeHTTP(w, r)
		return
	}
	ps := &e.plays
	ps.mu.Lock()
	pb := ps.byTicket[sha256.Sum256([]byte(ticket))]
	ps.mu.Unlock()
	if pb == nil {
		// A ticket of the host's own people, or one that ended: the
		// host's to answer (it answers 404 to one it doesn't know).
		fallback.ServeHTTP(w, r)
		return
	}
	notFound := func() { e.cfg.NotFound.ServeHTTP(w, r) }
	// GET only (HEAD refused too), no query, no browser: a browser must
	// never use a peer's ticket (MSFP media section 2).
	if r.Method != http.MethodGet || strings.Contains(r.RequestURI, "?") || r.URL.RawQuery != "" ||
		r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" || r.Header.Get("Sec-Fetch-Mode") != "" {
		notFound()
		return
	}
	path, ok := mediaPath(name, pb)
	if !ok || e.cfg.Media == nil || !e.playAllowed(r.Context(), pb) {
		notFound()
		return
	}
	var rng *Range
	if h := r.Header.Values("Range"); len(h) > 0 && (path.Kind == MediaFile || path.Kind == MediaSegment) {
		parsed, ok := ParseRange(h[0])
		if !ok || len(h) > 1 {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		rng = &parsed
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(pb.ctx, cancel)
	defer stop()
	freeOne, ok := e.fetchSlots.take(ctx, pb.id)
	if !ok {
		e.busy(w, ctx)
		return
	}
	defer freeOne()
	freeAll, ok := e.peerFetchSlots.take(ctx, pb.peer)
	if !ok {
		e.busy(w, ctx)
		return
	}
	defer freeAll()
	e.used(pb)
	ps.mu.Lock()
	pb.open++
	ps.mu.Unlock()
	defer func() {
		ps.mu.Lock()
		pb.open--
		ps.mu.Unlock()
		e.used(pb)
	}()
	r = r.WithContext(ctx)
	if path.Kind == MediaMaster || path.Kind == MediaPlaylist {
		e.servePlaylist(w, r, pb, path)
		return
	}
	sink := &httpSink{w: w, ctx: ctx}
	err := e.cfg.Media.Serve(ctx, pb.handle, path, rng, sink)
	if sink.started || ctx.Err() != nil {
		return
	}
	e.mediaError(w, r, err)
}

// busy answers 503 to a fetch over the bounds, unless it was cut.
func (e *Engine) busy(w http.ResponseWriter, ctx context.Context) {
	if ctx.Err() == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
}

// mediaError answers a media request the host failed: its NotFound,
// RangeNotSatisfiable and ServerBusy as 404, 416 and 503; anything else
// 500, logged.
func (e *Engine) mediaError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case err == nil:
		e.log.Warn("msfp: the host's media answered nothing")
		w.WriteHeader(http.StatusInternalServerError)
	case errors.Is(err, ErrNotFound):
		e.cfg.NotFound.ServeHTTP(w, r)
	case errors.Is(err, ErrRangeNotSatisfiable):
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	case errors.Is(err, ErrServerBusy):
		w.WriteHeader(http.StatusServiceUnavailable)
	default:
		e.log.Warn("msfp: the host's media failed", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
	}
}

// servePlaylist serves a playlist the host made, once checked against
// media 1's subset (MSFP media section 3.1): one the host made outside it
// is refused, with a warning, rather than refused by every peer.
func (e *Engine) servePlaylist(w http.ResponseWriter, r *http.Request, pb *playback, path MediaPath) {
	var buf boundedSink
	buf.max = wire.MaxPlaylist
	err := e.cfg.Media.Serve(r.Context(), pb.handle, path, nil, &buf)
	if !buf.started {
		e.mediaError(w, r, err)
		return
	}
	if err != nil || buf.over || buf.status != http.StatusOK {
		e.log.Warn("msfp: the host's playlist couldn't be served", "error", err, "status", buf.status)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	subset := wire.MediaSubset
	if path.Kind == MediaMaster {
		subset = wire.MasterSubset
	}
	kept, err := wire.FilterPlaylist(buf.body.Bytes(), subset)
	if err == nil && !bytes.Equal(kept, normalLines(buf.body.Bytes())) {
		err = errors.New("tags outside media 1's subset")
	}
	if err != nil {
		e.log.Warn("msfp: the host made a playlist outside what MSFP allows; it isn't sent", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/vnd.apple.mpegurl")
	h.Set("Content-Length", strconv.Itoa(len(kept)))
	w.WriteHeader(http.StatusOK)
	w.Write(kept)
}

// normalLines is a playlist with its empty lines dropped, each line
// ending in a line feed, as FilterPlaylist writes what it keeps.
func normalLines(b []byte) []byte {
	var out bytes.Buffer
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimSuffix(line, "\r"); line != "" {
			out.WriteString(line + "\n")
		}
	}
	return out.Bytes()
}

// NewHTTPSink is a ResponseSink on an http.ResponseWriter, for a host's
// viewer route around Fetch: the status and only the headers MSFP needs
// (Content-Type, Content-Length, Content-Range, Accept-Ranges), no
// content coding. Writes fail once ctx is done.
func NewHTTPSink(ctx context.Context, w http.ResponseWriter) ResponseSink {
	return &httpSink{w: w, ctx: ctx}
}

// httpSink is a ResponseSink on an http.ResponseWriter: the status and
// only the headers MSFP needs, no content coding.
type httpSink struct {
	w       http.ResponseWriter
	ctx     context.Context
	started bool
}

func (s *httpSink) Start(status int, contentType string, length int64, contentRange string) {
	if s.started {
		return
	}
	s.started = true
	h := s.w.Header()
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	if length >= 0 {
		h.Set("Content-Length", strconv.FormatInt(length, 10))
	}
	if contentRange != "" {
		h.Set("Content-Range", contentRange)
	}
	if status == http.StatusOK || status == http.StatusPartialContent {
		h.Set("Accept-Ranges", "bytes")
	}
	s.w.WriteHeader(status)
}

func (s *httpSink) Write(b []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		// The playback ended, or the peer left: nothing more goes.
		return 0, err
	}
	if !s.started {
		s.Start(http.StatusOK, "", -1, "")
	}
	return s.w.Write(b)
}

// boundedSink is a ResponseSink that keeps an answer in memory, up to
// max bytes.
type boundedSink struct {
	max          int
	started      bool
	over         bool
	status       int
	contentType  string
	length       int64
	contentRange string
	body         bytes.Buffer
}

func (s *boundedSink) Start(status int, contentType string, length int64, contentRange string) {
	if !s.started {
		s.started, s.status, s.contentType, s.length, s.contentRange = true, status, contentType, length, contentRange
	}
}

func (s *boundedSink) Write(b []byte) (int, error) {
	if !s.started {
		s.Start(http.StatusOK, "", -1, "")
	}
	if s.body.Len()+len(b) > s.max {
		s.over = true
		return 0, errors.New("msfp: an answer over its bound")
	}
	return s.body.Write(b)
}
