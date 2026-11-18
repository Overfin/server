package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/msfp"
	"github.com/overfin/server/msfp/wire"
)

// Stream serves what a playback session plays: the media file, rewritten
// HLS playlists, segments and subtitles. Every response is looked up through
// the stream ticket; the real Jellyfin URLs, which carry its token, never
// leave the server (SECURITY.md rules 9, 14, 17 and 18).
type Stream struct {
	Playback Playback
}

func (Stream) Name() string { return "stream" }

// Register declares the stream's routes as media routes: they check their
// ticket themselves. A ticket of this server's own playbacks works only in
// the session that started it (whose cookie never goes along from another
// site); one a paired server's viewer plays, for that server only, never
// for a browser (rule 46).
func (s Stream) Register(r *plugin.Registrar) error {
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /api/v1/stream/{ticket}/{name}":                   s.serve,
		"GET /api/v1/stream/{ticket}/subtitles/{track}/{part}": s.subtitles,
		"GET /api/v1/stream/{ticket}/trickplay/{sheet}":        s.trickplay,
		"GET /api/v1/stream/{ticket}/pictures/{track}/{part}":  s.pictures,
	} {
		if err := r.HandleMedia(pattern, s.peersFirst(h)); err != nil {
			return err
		}
	}
	return nil
}

// peersFirst serves a paired server's ticket through the MSFP engine
// (msfp.Engine.StreamHandler), which checks it and the request before
// PeerHost.Serve serves a byte; any other ticket, this server's own
// people's, goes to h.
func (s Stream) peersFirst(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if eng := s.Playback.engine(); eng != nil {
			eng.StreamHandler(h).ServeHTTP(w, r)
			return
		}
		h(w, r)
	})
}

// trickplay serves a sheet of a playback's seek-bar previews by its
// ticket: what a paired server's viewer's server fetches (MSFP video,
// section 5.5), checked as the stream is. A paired server's playback here has its
// sheets at this server's playback route only.
func (s Stream) trickplay(w http.ResponseWriter, r *http.Request) {
	if hasQuery(r) {
		problem.Send(w, problem.NotFound, "")
		return
	}
	st, _, done := s.stream(w, r)
	defer done()
	if st == nil {
		return
	}
	if st.Remote != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	s.Playback.serveSheet(w, r, st, r.PathValue("sheet"))
}

var (
	ticketRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	rangeRe  = regexp.MustCompile(`^bytes=\d{0,15}-\d{0,15}$`)
)

// peerTicket says whether a playback here is a paired server's viewer's.
func peerTicket(st *playback.Stream) bool {
	return strings.HasPrefix(st.SessionID, playback.PeerPrefix)
}

func nothing() {}

// stream finds the playback behind a ticket, for its own session only,
// and gives what to call once the request is answered (it frees a paired
// server's slots).
func (s Stream) stream(w http.ResponseWriter, r *http.Request) (*playback.Stream, *jellyfin.Auth, func()) {
	st, a, done := s.find(w, r)
	if done == nil {
		done = nothing
	}
	return st, a, done
}

func (s Stream) find(w http.ResponseWriter, r *http.Request) (*playback.Stream, *jellyfin.Auth, func()) {
	// A paired server's viewer, through that server, as the MSFP engine
	// found it by its ticket and checked the request (PeerHost.Serve):
	// no session here.
	if st, ok := r.Context().Value(peerStreamKey{}).(*playback.Stream); ok {
		a := st.Auth
		return st, &a, nil
	}
	ticket := r.PathValue("ticket")
	if !ticketRe.MatchString(ticket) {
		problem.Send(w, problem.NotFound, "")
		return nil, nil, nil
	}
	// This server's own viewers ask from its own site: a request another
	// site starts (a link, an embedded video) goes no further, so it never
	// starts a session or replaces the cookie (media routes are let through
	// for paired servers' viewers).
	if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "same-site" {
		problem.Send(w, problem.NotFound, "")
		return nil, nil, nil
	}
	l := s.Playback.Library
	c := l.Sessions.Current(w, r)
	if c == nil {
		return nil, nil, nil
	}
	p := c.Profile
	// The ticket works only in the session that started the playback
	// (rule 45): a copied stream URL is useless anywhere else, and doesn't
	// keep the playback alive either (rule 50).
	st, ok := s.Playback.Tickets.ByTicket(ticket, c.Session.ID, p.ID, time.Now())
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return nil, nil, nil
	}
	// As the playback's own device, so its transcode is the one served.
	a := st.Auth
	return st, &a, nil
}

func (s Stream) serve(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if hasQuery(r) || !wire.FileName(name) {
		problem.Send(w, problem.NotFound, "")
		return
	}
	st, a, done := s.stream(w, r)
	defer done()
	if st == nil {
		return
	}
	if st.Remote != nil {
		path, ok := msfp.ParseMediaPath(name)
		if !ok {
			problem.Send(w, problem.NotFound, "")
			return
		}
		s.fetchRemote(w, r, st, path)
		return
	}
	dec := st.Decision
	switch {
	case name == "file" && dec.Protocol == "file":
		s.proxy(w, r, st, *a, dec.Upstream, true, "")
	case name == "master.m3u8" && dec.Protocol == "hls":
		s.playlist(w, r, st, *a, dec.Upstream, playback.RewriteMaster)
	case strings.HasSuffix(name, ".m3u8"):
		up, ok := st.File(name)
		if !ok {
			problem.Send(w, problem.NotFound, "")
			return
		}
		s.playlist(w, r, st, *a, up, playback.RewriteVariant)
	default:
		up, ok := st.File(name)
		if !ok {
			problem.Send(w, problem.NotFound, "")
			return
		}
		contentType := "video/mp4"
		switch {
		case strings.HasSuffix(name, ".ts"):
			contentType = "video/mp2t"
		case strings.HasSuffix(name, ".aac"):
			contentType = "audio/aac"
		case strings.HasSuffix(name, ".mp3"):
			contentType = "audio/mpeg"
		}
		// A paired server may ask a segment by range too (MSFP media,
		// section 2.1); a browser here, the whole segment.
		s.proxy(w, r, st, *a, up, peerTicket(st), contentType)
	}
}

// playlist fetches an upstream playlist, rewrites it and remembers the real
// URLs behind its new names.
func (s Stream) playlist(w http.ResponseWriter, r *http.Request, st *playback.Stream, a jellyfin.Auth,
	up playback.Upstream, rewrite func([]byte, playback.Upstream) (*playback.Rewritten, error)) {

	l := s.Playback.Library
	body, err := l.Jellyfin.ReadSmall(r.Context(), a, up.Path, up.Query, wire.MaxPlaylist)
	if err != nil {
		s.upstreamError(w, st, err)
		return
	}
	rw, err := rewrite(body, up)
	if err != nil {
		l.Logger.Warn("refused an upstream playlist", "playback", st.ID, "err", err)
		if peerTicket(st) {
			problem.Send(w, problem.NotFound, "") // a media status (MSFP core, section 8.4)
			return
		}
		problem.Send(w, problem.NotPlayable, "")
		return
	}
	st.AddFiles(rw.Files)
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Content-Length", strconv.Itoa(len(rw.Body)))
	_, _ = w.Write(rw.Body)
}

// proxy streams bytes from upstream without buffering. Only the headers
// streaming needs are passed on (rule 18); for direct play the client's
// Range header is forwarded so seeking works.
func (s Stream) proxy(w http.ResponseWriter, r *http.Request, st *playback.Stream, a jellyfin.Auth, up playback.Upstream,
	allowRange bool, forceType string) {

	rangeHeader := ""
	if allowRange {
		if rh := r.Header.Get("Range"); rh != "" {
			// A paired server's, exactly MSFP's three forms (media section
			// 2.1); a browser's, any one range Jellyfin answers.
			if (peerTicket(st) && !validPeerRange(rh)) || !validRange(rh) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			rangeHeader = rh
		}
	}
	// A playback that ends takes its response along: stopped by an admin,
	// taken over or idle, a file already flowing mustn't go on, past the
	// limits (rules 45 and 49).
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		select {
		case <-st.Over():
			cancel()
		case <-ctx.Done():
		}
	}()
	l := s.Playback.Library
	res, err := l.Jellyfin.Open(ctx, a, up.Path, up.Query, rangeHeader)
	if err != nil {
		s.upstreamError(w, st, err)
		return
	}
	defer res.Body.Close()
	// To a paired server, only the statuses MSFP media has (core section
	// 8.4): what else Jellyfin answers is a 404, its 5xx a 503.
	if peerTicket(st) {
		switch code := res.StatusCode; {
		case code == http.StatusOK, code == http.StatusPartialContent, code == http.StatusRequestedRangeNotSatisfiable:
		case code >= 500:
			problem.Send(w, problem.ServerBusy, "")
			return
		default:
			problem.Send(w, problem.NotFound, "")
			return
		}
	}

	h := w.Header()
	contentType := forceType
	if contentType == "" {
		contentType = mediaType(res.Header.Get("Content-Type"))
	}
	h.Set("Content-Type", contentType)
	for _, k := range []string{"Content-Length", "Content-Range", "Accept-Ranges"} {
		if v := res.Header.Get(k); v != "" && len(v) < 200 {
			h.Set(k, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	if r.Method == http.MethodHead {
		return // the headers only: the file isn't read through for nothing
	}
	// io.Copy streams in small chunks: memory stays flat however long the
	// film. If the client goes away, or the playback ends, the context ends
	// the upstream read too. While the response is open, flowing or held
	// (a paused player with its buffer full reads nothing), the playback is
	// in use: a paired server's viewer sends no progress here, and a film
	// played straight is often one long download.
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case now := <-tick.C:
				s.Playback.Tickets.Touch(st, now)
			case <-ctx.Done():
				return
			}
		}
	}()
	_, _ = io.Copy(w, res.Body)
}

// mediaType keeps an upstream content type only when it is audio or video,
// as MSFP media section 3 says of what a viewer's server relays (letters,
// digits and .+- only, never a playlist's); anything else is sent as
// opaque bytes, never as something a browser would render (and nosniff is
// set on every response).
func mediaType(ct string) string { return wire.RelayedMediaType(ct) }

// subtitleReads bounds how many subtitle tracks are held in memory at
// once: each is read whole before it is checked (a PGS track may be up to
// wire.MaxPGS), so many asked together could otherwise take hundreds
// of megabytes. A read waits for a slot as long as its request lasts; the
// slot is freed once the track is checked, before it's sent.
var subtitleReads = make(chan struct{}, 4)

// subtitleSlot takes a slot, and gives the function that frees it; false
// when the request ended while waiting.
func subtitleSlot(r *http.Request) (func(), bool) { return subtitleSlotWithin(r.Context()) }

// subtitleSlotWithin is subtitleSlot, waiting as long as ctx lasts.
func subtitleSlotWithin(ctx context.Context) (func(), bool) {
	select {
	case subtitleReads <- struct{}{}:
		return func() { <-subtitleReads }, true
	case <-ctx.Done():
		return nil, false
	}
}

// Subtitles go to the player in stretches of StretchSeconds, part n
// covering [n*StretchSeconds, (n+1)*StretchSeconds) of the video: the
// plugin reads each from there, never the whole file. Each starts
// stretchLead earlier, so a subtitle already on screen when its stretch
// begins comes with it; the player drops the doubles.
const (
	StretchSeconds = 120
	stretchLead    = 10
	// maxPart bounds a part's number: 10,000 stretches of 2 minutes is
	// over 13 days of video.
	maxPart = 10000
)

// subtitlePart reads the {part} of a subtitle route.
func subtitlePart(r *http.Request) (int, bool) {
	v := r.PathValue("part")
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n >= maxPart || strconv.Itoa(n) != v {
		return 0, false
	}
	return n, true
}

// stretch fetches one stretch of a subtitle track of this server's own
// playback from Overfin's plugin, or answers the error itself (nil). The
// caller holds a subtitle slot.
func (s Stream) stretch(w http.ResponseWriter, r *http.Request, st *playback.Stream, index, part, limit int) []byte {
	l := s.Playback.Library
	if l.Service == nil {
		problem.Send(w, problem.Internal, "")
		return nil
	}
	from := max(float64(part*StretchSeconds-stretchLead), 0)
	to := float64((part + 1) * StretchSeconds)
	body, err := l.Jellyfin.SubtitleStretch(r.Context(), l.Service.Admin(), st.ItemID, st.SourceID, index, from, to, limit)
	if err != nil {
		s.upstreamError(w, st, err)
		return nil
	}
	return body
}

// subtitles serves one stretch of a text subtitle track as sanitized
// WebVTT.
func (s Stream) subtitles(w http.ResponseWriter, r *http.Request) {
	track := r.PathValue("track")
	if hasQuery(r) {
		problem.Send(w, problem.NotFound, "")
		return
	}
	index, ok := trackIndex(track, 's')
	part, okPart := subtitlePart(r)
	if !ok || !okPart {
		problem.Send(w, problem.NotFound, "")
		return
	}
	st, _, done := s.stream(w, r)
	defer done()
	if st == nil {
		return
	}
	if st.Remote != nil {
		s.fetchRemote(w, r, st, msfp.MediaPath{Kind: msfp.MediaSubtitles, Track: track, Part: part})
		return
	}
	if !st.TextSubtitle(index) {
		problem.Send(w, problem.NotFound, "")
		return
	}
	free, ok := subtitleSlot(r)
	if !ok {
		return
	}
	free = sync.OnceFunc(free)
	defer free()
	body := s.stretch(w, r, st, index, part, wire.MaxWebVTT)
	if body == nil {
		return
	}
	vtt, ok := wire.RebuildWebVTT(body)
	if !ok {
		s.Playback.Library.Logger.Warn("refused upstream subtitles", "playback", st.ID)
		problem.Send(w, problem.NotFound, "")
		return
	}
	free() // checked: the slot is for reading, not for the client's pace
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(vtt)))
	_, _ = w.Write(vtt)
}

// pictures serves one stretch of a subtitle track of pictures (PGS) as
// Overfin's plugin copies it, once Overfin read it whole and found every
// part of it well-formed and within its limits (SECURITY.md rule 17). The
// player decodes and draws it: sent as opaque bytes, never as anything a
// browser would render.
func (s Stream) pictures(w http.ResponseWriter, r *http.Request) {
	track := r.PathValue("track")
	if hasQuery(r) {
		problem.Send(w, problem.NotFound, "")
		return
	}
	index, ok := trackIndex(track, 's')
	part, okPart := subtitlePart(r)
	if !ok || !okPart {
		problem.Send(w, problem.NotFound, "")
		return
	}
	st, _, done := s.stream(w, r)
	defer done()
	if st == nil {
		return
	}
	if st.Remote != nil {
		s.fetchRemote(w, r, st, msfp.MediaPath{Kind: msfp.MediaPictures, Track: track, Part: part})
		return
	}
	if !st.PictureSubtitle(index) {
		problem.Send(w, problem.NotFound, "")
		return
	}
	free, ok := subtitleSlot(r)
	if !ok {
		return
	}
	free = sync.OnceFunc(free)
	defer free()
	body := s.stretch(w, r, st, index, part, wire.MaxPGS)
	if body == nil {
		return
	}
	if err := wire.CheckPGS(body); err != nil {
		s.Playback.Library.Logger.Warn("refused upstream picture subtitles", "playback", st.ID)
		problem.Send(w, problem.NotFound, "")
		return
	}
	free() // checked: the slot is for reading, not for the client's pace
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
}

// validRange says whether a Range header is one range of bytes Jellyfin
// can answer: from a byte, to one, or both in order (not "bytes=-", nor
// "bytes=5-3").
func validRange(h string) bool {
	if !rangeRe.MatchString(h) {
		return false
	}
	from, to, _ := strings.Cut(strings.TrimPrefix(h, "bytes="), "-")
	switch {
	case from == "" && to == "":
		return false
	case from == "" || to == "":
		return true
	}
	a, _ := strconv.ParseInt(from, 10, 64)
	b, _ := strconv.ParseInt(to, 10, 64)
	return a <= b
}

// validPeerRange says whether a Range header is one of MSFP's three forms
// (media section 2.1): never several ranges, "bytes=-" or "bytes=-0".
func validPeerRange(h string) bool {
	_, ok := msfp.ParseRange(h)
	return ok
}

// hasQuery says whether a request's URL has a query at all: "?", "?&"
// and "?;" too, which parse to no values (MSFP media, section 2: any
// query is a 404).
func hasQuery(r *http.Request) bool {
	return r.URL.RawQuery != "" || r.URL.ForceQuery
}

// upstreamError answers a failed Jellyfin call for a stream: to a paired
// server, only MSFP media's statuses (core section 8.4), a 404 for what
// Jellyfin refused, a 503 for the rest; to this server's own viewers, as
// any other call.
func (s Stream) upstreamError(w http.ResponseWriter, st *playback.Stream, err error) {
	l := s.Playback.Library
	if !peerTicket(st) {
		l.upstreamError(w, err)
		return
	}
	var se *jellyfin.StatusError
	switch {
	case errors.Is(err, context.Canceled):
	case errors.As(err, &se) && se.Status >= 400 && se.Status < 500:
		if se.Status == http.StatusUnauthorized && l.Service != nil {
			// Jellyfin refused the account's token: a new one, as for any call.
			go func() {
				if err := l.Service.Refused(context.Background()); err != nil {
					l.Logger.Error("renewing the Jellyfin account's token failed", "err", err)
				}
			}()
		}
		problem.Send(w, problem.NotFound, "")
	default:
		l.Logger.Warn("a paired server's stream: Jellyfin failed", "playback", st.ID, "err", err)
		problem.Send(w, problem.ServerBusy, "")
	}
}

// A paired server's title plays through this server (rule 97): the
// viewer's browser asks this server only, with this server's ticket, and
// the MSFP engine asks the paired one with the ticket it gave, which
// never leaves the engine. The paired server sees this server, never the
// viewer.

// fetchRemote answers a browser's request for a paired server's playback
// with what the engine fetches of it (msfp.Engine.Fetch), checked as
// anything from Jellyfin is: playlists within MSFP's subset, subtitles
// sanitized, sheets re-encoded, media passed on with the headers
// streaming needs, a 206 only for the range asked (MSFP media, section
// 2). A failed fetch is a 404 to the browser, never the paired server's
// body; its 416 and 503 are passed on as such. HEAD fetches nothing.
func (s Stream) fetchRemote(w http.ResponseWriter, r *http.Request, st *playback.Stream, path msfp.MediaPath) {
	eng := s.Playback.engine()
	if eng == nil || r.Method != http.MethodGet {
		problem.Send(w, problem.NotFound, "")
		return
	}
	// A playback that ends takes its response along (rules 45 and 49).
	// While a response is open, the playback is in use: a film played
	// straight is often one long download, with no progress between.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-st.Over():
				cancel()
				return
			case now := <-tick.C:
				s.Playback.Tickets.Touch(st, now)
			case <-ctx.Done():
				return
			}
		}
	}()
	s.Playback.Tickets.Touch(st, time.Now())
	err := eng.Fetch(ctx, st.Remote.ID, path, r.Header.Get("Range"), msfp.NewHTTPSink(ctx, w))
	switch {
	case err == nil, ctx.Err() != nil:
	case errors.Is(err, msfp.ErrRangeNotSatisfiable):
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	case errors.Is(err, msfp.ErrServerBusy):
		problem.Send(w, problem.ServerBusy, "")
	default:
		problem.Send(w, problem.NotFound, "")
	}
}
