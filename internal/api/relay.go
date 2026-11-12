package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/problem"
)

// A paired server's title plays through this server (rule 65): the
// viewer's browser asks this server only, with this server's ticket, and
// this server asks the paired one with the ticket it gave, which never
// leaves here. The paired server sees this server, never the viewer.

// relayTransport asks paired servers for their streams: no proxy from
// the environment, a minute to answer, then as long as the stream lasts.
var relayTransport = &http.Transport{
	DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: 60 * time.Second,
	MaxIdleConnsPerHost:   8,
	IdleConnTimeout:       90 * time.Second,
}

// relayClients are the clients with the certificates federation trusts,
// when it was given others than the system's (a test's), by TLS config.
var relayClients sync.Map

// relayClient is the client for a paired server's stream: never following
// redirects, trusting the certificates federation's client does.
func (s Stream) relayClient() *http.Client {
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if fed := s.Playback.fed(); fed != nil && fed.HTTP != nil {
		if t, ok := fed.HTTP.Transport.(*http.Transport); ok && t.TLSClientConfig != nil {
			if c, ok := relayClients.Load(t.TLSClientConfig); ok {
				return c.(*http.Client)
			}
			tr := relayTransport.Clone()
			tr.TLSClientConfig = t.TLSClientConfig.Clone()
			c, _ := relayClients.LoadOrStore(t.TLSClientConfig, &http.Client{Transport: tr, CheckRedirect: noRedirect})
			return c.(*http.Client)
		}
	}
	return &http.Client{Transport: relayTransport, CheckRedirect: noRedirect}
}

// relayed is what the browser asks of a paired server's playback: a file
// of its stream ("master.m3u8", a segment, "file"), or a subtitle track.
type relayed struct {
	path     string // after the stream's base: "master.m3u8", "subtitles/s3"…
	playlist bool
	vtt      bool
	pgs      bool
}

// relay answers a browser's request for a paired server's stream with
// what that server gives, checked as anything from Jellyfin is: playlists
// naming only files of the stream, subtitles sanitized, media passed on
// with the headers streaming needs.
func (s Stream) relay(w http.ResponseWriter, r *http.Request, st *playback.Stream, what relayed) {
	rm := st.Remote
	if rm == nil || rm.Base == "" {
		problem.Send(w, problem.NotFound, "")
		return
	}
	l := s.Playback.Library
	// A playback that ends takes its response along (rules 30 and 32).
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		select {
		case <-st.Over():
			cancel()
		case <-ctx.Done():
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rm.Base+what.path, nil)
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	media := !what.playlist && !what.vtt && !what.pgs
	if rh := r.Header.Get("Range"); rh != "" && media {
		if !validRange(rh) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		req.Header.Set("Range", rh)
	}
	if !media {
		free, ok := subtitleSlot(r)
		if !ok {
			return
		}
		free = sync.OnceFunc(free)
		defer free()
	}
	res, err := s.relayClient().Do(req)
	if err != nil {
		if ctx.Err() == nil {
			l.Logger.Warn("a paired server's stream didn't answer", "playback", st.ID, "err", err)
		}
		problem.Send(w, problem.ServerBusy, "")
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusPartialContent {
		problem.Send(w, problem.NotFound, "")
		return
	}
	if !media {
		limit := int64(maxPlaylist)
		switch {
		case what.vtt:
			limit = maxSubtitles
		case what.pgs:
			limit = playback.MaxPGS
		}
		body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
		if err != nil || int64(len(body)) > limit {
			problem.Send(w, problem.NotPlayable, "")
			return
		}
		contentType := "application/vnd.apple.mpegurl"
		switch {
		case what.playlist:
			if err := checkRelayedPlaylist(body); err != nil {
				l.Logger.Warn("refused a paired server's playlist", "playback", st.ID, "err", err)
				problem.Send(w, problem.NotPlayable, "")
				return
			}
		case what.vtt:
			vtt, ok := playback.SanitizeVTT(body)
			if !ok {
				problem.Send(w, problem.NotFound, "")
				return
			}
			body, contentType = vtt, "text/vtt; charset=utf-8"
		case what.pgs:
			if playback.CheckPGS(body) != nil {
				problem.Send(w, problem.NotFound, "")
				return
			}
			contentType = "application/octet-stream"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
		return
	}
	h := w.Header()
	h.Set("Content-Type", mediaType(res.Header.Get("Content-Type")))
	for _, k := range []string{"Content-Length", "Content-Range", "Accept-Ranges"} {
		if v := res.Header.Get(k); v != "" && len(v) < 200 {
			h.Set(k, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	// Held open (a paused player reads nothing), the playback is in use.
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

var playlistURIRe = regexp.MustCompile(`URI="([^"]*)"`)

// checkRelayedPlaylist accepts a paired server's playlist only when every
// address in it is a file of the same stream, by name (no path, no other
// server): what the paired server's own rewriting gives (rule 8).
func checkRelayedPlaylist(body []byte) error {
	if !bytes.HasPrefix(body, []byte("#EXTM3U")) {
		return errors.New("not a playlist")
	}
	for line := range strings.SplitSeq(string(body), "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case line == "":
		case strings.HasPrefix(line, "#"):
			for _, m := range playlistURIRe.FindAllStringSubmatch(line, -1) {
				if !fileNameRe.MatchString(m[1]) {
					return errors.New("an address that isn't the stream's")
				}
			}
		case !fileNameRe.MatchString(line):
			return errors.New("an address that isn't the stream's")
		}
	}
	return nil
}
