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
	if err := r.HandleMedia("GET /api/v1/stream/{ticket}/{name}", http.HandlerFunc(s.serve)); err != nil {
		return err
	}
	if err := r.HandleMedia("GET /api/v1/stream/{ticket}/subtitles/{track}/{part}", http.HandlerFunc(s.subtitles)); err != nil {
		return err
	}
	return r.HandleMedia("GET /api/v1/stream/{ticket}/pictures/{track}/{part}", http.HandlerFunc(s.pictures))
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
	// A paired server's viewer, through that server: no session here.
	if st, ok := s.Playback.Tickets.ByPeerTicket(ticket, time.Now()); ok {
		// Asked by that server, never by a browser (rule 46): a browser
		// says where a request comes from. And only while the pairing
		// lasts: an ended one ends its playbacks.
		if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" || r.Header.Get("Sec-Fetch-Mode") != "" {
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
	// (rule 45): a copied stream URL is useless anywhere else, and doesn't
	// keep the playback alive either (rule 50).
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
	if st.Remote != nil {
		s.relay(w, r, st, relayed{path: name, playlist: strings.HasSuffix(name, ".m3u8")})
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
// streaming needs are passed on (rule 18); for direct play the client's
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
		l.upstreamError(w, err)
		return nil
	}
	return body
}

// subtitles serves one stretch of a text subtitle track as sanitized
// WebVTT.
func (s Stream) subtitles(w http.ResponseWriter, r *http.Request) {
	track := r.PathValue("track")
	if len(r.URL.Query()) > 0 {
		problem.Send(w, problem.NotFound, "")
		return
	}
	index, ok := trackIndex(track, 's')
	part, okPart := subtitlePart(r)
	if !ok || !okPart {
		problem.Send(w, problem.NotFound, "")
		return
	}
	st, _ := s.stream(w, r)
	if st == nil {
		return
	}
	if st.Remote != nil {
		s.relay(w, r, st, relayed{path: "subtitles/" + track + "/" + strconv.Itoa(part), vtt: true})
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
	body := s.stretch(w, r, st, index, part, maxSubtitles)
	if body == nil {
		return
	}
	vtt, ok := playback.SanitizeVTT(body)
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
	if len(r.URL.Query()) > 0 {
		problem.Send(w, problem.NotFound, "")
		return
	}
	index, ok := trackIndex(track, 's')
	part, okPart := subtitlePart(r)
	if !ok || !okPart {
		problem.Send(w, problem.NotFound, "")
		return
	}
	st, _ := s.stream(w, r)
	if st == nil {
		return
	}
	if st.Remote != nil {
		s.relay(w, r, st, relayed{path: "pictures/" + track + "/" + strconv.Itoa(part), pgs: true})
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
	body := s.stretch(w, r, st, index, part, playback.MaxPGS)
	if body == nil {
		return
	}
	if err := playback.CheckPGS(body); err != nil {
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
