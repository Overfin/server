package api

import (
	"context"
	"errors"
	"github.com/overfin/server/internal/store"
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
)

// Stream serves what a playback session plays: the media file, rewritten
// HLS playlists, segments and subtitles. Every response is looked up through
// the stream ticket; the real Jellyfin URLs, which carry its token, never
// leave the server (SECURITY.md rules 4, 8, 11 and 12).
type Stream struct {
	Playback Playback
}

func (Stream) Name() string { return "stream" }

// Register declares the stream's routes as media routes: they check their
// ticket themselves. A ticket of this server's own playbacks works only in
// the session that started it (whose cookie never goes along from another
// site); one a paired server's viewer plays works from that server's site
// only (rule 65).
func (s Stream) Register(r *plugin.Registrar) error {
	if err := r.HandleMedia("GET /api/v1/stream/{ticket}/{name}", http.HandlerFunc(s.serve)); err != nil {
		return err
	}
	if err := r.HandleMedia("GET /api/v1/stream/{ticket}/subtitles/{track}", http.HandlerFunc(s.subtitles)); err != nil {
		return err
	}
	return r.HandleMedia("GET /api/v1/stream/{ticket}/pictures/{track}", http.HandlerFunc(s.pictures))
}

const (
	maxPlaylist  = 2 << 20
	maxSubtitles = 5 << 20
)

var (
	ticketRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	fileNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	rangeRe    = regexp.MustCompile(`^bytes=\d{0,15}-\d{0,15}$`)
)

// stream finds the playback behind a ticket, for its own session only.
func (s Stream) stream(w http.ResponseWriter, r *http.Request) (*playback.Stream, *jellyfin.Auth) {
	ticket := r.PathValue("ticket")
	if !ticketRe.MatchString(ticket) {
		problem.Send(w, problem.NotFound, "")
		return nil, nil
	}
	// A paired server's viewer, from its site: no session here.
	if st, ok := s.Playback.Tickets.ByPeerTicket(ticket, time.Now()); ok {
		// Only from that server's site, when the browser says where it
		// comes from; and only while the pairing lasts: an ended one ends
		// its playbacks.
		if o := r.Header.Get("Origin"); o != "" && o != st.Origin {
			problem.Send(w, problem.NotFound, "")
			return nil, nil
		}
		peer, err := s.Playback.Library.Provisioner.Store.PeerByID(r.Context(), strings.TrimPrefix(st.ProfileID, playback.PeerPrefix))
		if err != nil || peer.State != store.PeerPaired {
			if errors.Is(err, store.ErrNotFound) || err == nil {
				s.Playback.end(st, "pairing ended", "")
			}
			problem.Send(w, problem.NotFound, "")
			return nil, nil
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", st.Origin)
		h.Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
		h.Add("Vary", "Origin")
		a := st.Auth
		return st, &a
	}
	// This server's own viewers ask from its own site: a request another
	// site starts (a link, an embedded video) goes no further, so it never
	// starts a session or replaces the cookie (media routes are let through
	// for paired servers' viewers).
	if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "same-site" {
		problem.Send(w, problem.NotFound, "")
		return nil, nil
	}
	l := s.Playback.Library
	c := l.Sessions.Current(w, r)
	if c == nil {
		return nil, nil
	}
	p := c.Profile
	// The ticket works only in the session that started the playback
	// (rule 30): a copied stream URL is useless anywhere else, and doesn't
	// keep the playback alive either (rule 33).
	st, ok := s.Playback.Tickets.ByTicket(ticket, c.Session.ID, p.ID, time.Now())
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return nil, nil
	}
	// As the playback's own device, so its transcode is the one served.
	a := st.Auth
	return st, &a
}

func (s Stream) serve(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if len(r.URL.Query()) > 0 || !fileNameRe.MatchString(name) {
		problem.Send(w, problem.NotFound, "")
		return
	}
	st, a := s.stream(w, r)
	if st == nil {
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
		s.proxy(w, r, st, *a, up, false, contentType)
	}
}

// playlist fetches an upstream playlist, rewrites it and remembers the real
// URLs behind its new names.
func (s Stream) playlist(w http.ResponseWriter, r *http.Request, st *playback.Stream, a jellyfin.Auth,
	up playback.Upstream, rewrite func([]byte, playback.Upstream) (*playback.Rewritten, error)) {

	l := s.Playback.Library
	body, err := l.Jellyfin.ReadSmall(r.Context(), a, up.Path, up.Query, maxPlaylist)
	if err != nil {
		l.upstreamError(w, err)
		return
	}
	rw, err := rewrite(body, up)
	if err != nil {
		l.Logger.Warn("refused an upstream playlist", "playback", st.ID, "err", err)
		problem.Send(w, problem.NotPlayable, "")
		return
	}
	st.AddFiles(rw.Files)
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Content-Length", strconv.Itoa(len(rw.Body)))
	_, _ = w.Write(rw.Body)
}

// proxy streams bytes from upstream without buffering. Only the headers
// streaming needs are passed on (rule 12); for direct play the client's
// Range header is forwarded so seeking works.
func (s Stream) proxy(w http.ResponseWriter, r *http.Request, st *playback.Stream, a jellyfin.Auth, up playback.Upstream,
	allowRange bool, forceType string) {

	rangeHeader := ""
	if allowRange {
		if rh := r.Header.Get("Range"); rh != "" {
			if !validRange(rh) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			rangeHeader = rh
		}
	}
	// A playback that ends takes its response along: stopped by an admin,
	// taken over or idle, a file already flowing mustn't go on, past the
	// limits (rules 30 and 32).
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
		l.upstreamError(w, err)
		return
	}
	defer res.Body.Close()

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

// mediaType keeps an upstream content type only when it is audio or video;
// anything else is sent as opaque bytes, never as something a browser would
// render (and nosniff is set on every response).
func mediaType(ct string) string {
	t, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(ct)), ";")
	if (strings.HasPrefix(t, "video/") || strings.HasPrefix(t, "audio/")) && fileNameRe.MatchString(strings.ReplaceAll(t, "/", "-")) {
		return t
	}
	return "application/octet-stream"
}

// subtitleReads bounds how many subtitle tracks are held in memory at
// once: each is read whole before it is checked (a PGS track may be up to
// playback.MaxPGS), so many asked together could otherwise take hundreds
// of megabytes. A read waits for a slot as long as its request lasts; the
// slot is freed once the track is checked, before it's sent.
var subtitleReads = make(chan struct{}, 4)

// subtitleSlot takes a slot, and gives the function that frees it; false
// when the request ended while waiting.
func subtitleSlot(r *http.Request) (func(), bool) {
	select {
	case subtitleReads <- struct{}{}:
		return func() { <-subtitleReads }, true
	case <-r.Context().Done():
		return nil, false
	}
}

// subtitles serves one text subtitle track as sanitized WebVTT.
func (s Stream) subtitles(w http.ResponseWriter, r *http.Request) {
	track := r.PathValue("track")
	if len(r.URL.Query()) > 0 {
		problem.Send(w, problem.NotFound, "")
		return
	}
	index, ok := trackIndex(track, 's')
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	st, a := s.stream(w, r)
	if st == nil {
		return
	}
	if !st.TextSubtitle(index) {
		problem.Send(w, problem.NotFound, "")
		return
	}
	l := s.Playback.Library
	path := "/Videos/" + st.ItemID + "/" + st.SourceID + "/Subtitles/" + strconv.Itoa(index) + "/Stream.vtt"
	free, ok := subtitleSlot(r)
	if !ok {
		return
	}
	free = sync.OnceFunc(free)
	defer free()
	body, err := l.Jellyfin.ReadSubtitles(r.Context(), *a, path, maxSubtitles)
	if err != nil {
		l.upstreamError(w, err)
		return
	}
	vtt, ok := playback.SanitizeVTT(body)
	if !ok {
		l.Logger.Warn("refused upstream subtitles", "playback", st.ID)
		problem.Send(w, problem.NotFound, "")
		return
	}
	free() // checked: the slot is for reading, not for the client's pace
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(vtt)))
	_, _ = w.Write(vtt)
}

// pictures serves one subtitle track of pictures (PGS) as Jellyfin
// extracts it, once Overfin read it whole and found every part of it
// well-formed and within its limits (SECURITY.md rule 11). The player
// decodes and draws it: sent as opaque bytes, never as anything a browser
// would render.
func (s Stream) pictures(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()) > 0 {
		problem.Send(w, problem.NotFound, "")
		return
	}
	index, ok := trackIndex(r.PathValue("track"), 's')
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	st, a := s.stream(w, r)
	if st == nil {
		return
	}
	if !st.PictureSubtitle(index) {
		problem.Send(w, problem.NotFound, "")
		return
	}
	l := s.Playback.Library
	path := "/Videos/" + st.ItemID + "/" + st.SourceID + "/Subtitles/" + strconv.Itoa(index) + "/Stream.pgssub"
	free, ok := subtitleSlot(r)
	if !ok {
		return
	}
	free = sync.OnceFunc(free)
	defer free()
	body, err := l.Jellyfin.ReadSubtitles(r.Context(), *a, path, playback.MaxPGS)
	if err != nil {
		l.upstreamError(w, err)
		return
	}
	if err := playback.CheckPGS(body); err != nil {
		l.Logger.Warn("refused upstream picture subtitles", "playback", st.ID)
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
