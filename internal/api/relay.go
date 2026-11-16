package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // the sheets of seek-bar previews
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/overfin/server/internal/federation"
	"github.com/overfin/server/internal/outbound"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
)

// A paired server's title plays through this server (rule 97): the
// viewer's browser asks this server only, with this server's ticket, and
// this server asks the paired one with the ticket it gave, which never
// leaves here. The paired server sees this server, never the viewer.

// relayTransport asks paired servers for their streams: no proxy from
// the environment, a minute to answer, then as long as the stream lasts.
var relayTransport = &http.Transport{
	DialContext:           outbound.DialContext, // on the backend: through its gateway
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: 60 * time.Second,
	MaxIdleConnsPerHost:   8,
	IdleConnTimeout:       90 * time.Second,
}

// relayClients are the clients for each paired server's streams, by its
// ID: one per TLS settings federation gives for it (its certificates).
var relayClients sync.Map

type relayConn struct {
	tls    *tls.Config
	client *http.Client
}

// relayClient is the client for a paired server's stream: never following
// redirects, trusting what federation's calls to it would, except
// that a certificate of its own must be one it named (tls.go in
// federation): a ticket and media have only TLS to protect them.
func relayClient(fed *federation.Service, peer *store.Peer) *http.Client {
	cfg := fed.TLSConfig(peer)
	if c, ok := relayClients.Load(peer.ID); ok && c.(*relayConn).tls == cfg {
		return c.(*relayConn).client
	}
	tr := relayTransport.Clone()
	tr.TLSClientConfig = cfg
	c := &relayConn{tls: cfg, client: &http.Client{Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if old, ok := relayClients.Swap(peer.ID, c); ok {
		old.(*relayConn).client.CloseIdleConnections()
	}
	return c.client
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
	ctx, cancel := s.live(r, st)
	defer cancel()
	media := !what.playlist && !what.vtt && !what.pgs
	rh := r.Header.Get("Range")
	if rh != "" && media {
		if !validRange(rh) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
	} else {
		rh = ""
	}
	if !media {
		free, ok := subtitleSlot(r)
		if !ok {
			return
		}
		free = sync.OnceFunc(free)
		defer free()
	}
	res, prob, err := s.relayGet(ctx, st, what.path, rh)
	if err != nil {
		if prob == problem.ServerBusy && ctx.Err() == nil {
			l.Logger.Warn("a paired server's stream didn't answer", "playback", st.ID, "err", err)
		}
		problem.Send(w, prob, "")
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
			subset := mediaSubset
			if what.path == "master.m3u8" {
				subset = masterSubset
			}
			kept, err := relayedPlaylist(body, subset)
			if err != nil {
				l.Logger.Warn("refused a paired server's playlist", "playback", st.ID, "err", err)
				problem.Send(w, problem.NotPlayable, "")
				return
			}
			body = kept
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

// live is a request's context that a playback's end cancels too: a
// playback that ends takes its response along (rules 45 and 49).
func (s Stream) live(r *http.Request, st *playback.Stream) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(r.Context())
	go func() {
		select {
		case <-st.Over():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

var errNoRelay = errors.New("no paired server to ask")

// relayGet asks a paired server's playback for path, after its stream's
// base (with rangeHeader, when not ""): over the certificates federation
// trusts for it, once more when it names a renewed one. On an error, the
// problem to answer: not found, or busy when the server didn't answer.
func (s Stream) relayGet(ctx context.Context, st *playback.Stream, path, rangeHeader string) (*http.Response, problem.Type, error) {
	rm := st.Remote
	fed := s.Playback.fed()
	if rm == nil || rm.Base == "" || fed == nil {
		return nil, problem.NotFound, errNoRelay
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rm.Base+path, nil)
	if err != nil {
		return nil, problem.NotFound, withoutURL(err)
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	peer, err := fed.Store.PeerByID(ctx, rm.PeerID)
	if err != nil {
		return nil, problem.NotFound, err
	}
	res, err := relayClient(fed, peer).Do(req)
	if err != nil && ctx.Err() == nil {
		// Its certificate renewed: what it names now, and once more.
		if again, changed := fed.Repin(ctx, peer, err); changed {
			res, err = relayClient(fed, again).Do(req)
		}
	}
	if err != nil {
		return nil, problem.ServerBusy, withoutURL(err)
	}
	return res, "", nil
}

// withoutURL is err without the URL Go's HTTP client puts in it: a paired
// server's stream URL holds its ticket, which never goes into logs (MSFP
// media, section 1).
func withoutURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

// maxPeerSheet bounds a paired server's sheet of seek-bar previews, as
// read: a JPEG of at most 4,096 pixels a side is far smaller.
const maxPeerSheet = 8 << 20

// peerSheetReads bounds how many paired servers' sheets are held in
// memory at once (a player asks for all of a playback's at its start).
var peerSheetReads = make(chan struct{}, 4)

// relaySheet serves a sheet of a paired server's playback's seek-bar
// previews (rule 97) as that server gives it, when it is what the layout
// it announced says: a JPEG of at most maxPeerSheet bytes, no larger
// than a sheet of its tiles; re-encoded like every image (rule 16).
func (s Stream) relaySheet(w http.ResponseWriter, r *http.Request, st *playback.Stream, sheet string) {
	tp := st.Trickplay
	n, ok := sheetNumber(tp, sheet)
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	ctx, cancel := s.live(r, st)
	defer cancel()
	l := s.Playback.Library
	data, err := l.Images.Fetched(ctx, "peer-trickplay/"+st.ID+"/"+strconv.Itoa(n), func(ctx context.Context) ([]byte, error) {
		select {
		case peerSheetReads <- struct{}{}:
			defer func() { <-peerSheetReads }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		res, _, err := s.relayGet(ctx, st, "trickplay/"+strconv.Itoa(n), "")
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, errors.New("the paired server answered " + strconv.Itoa(res.StatusCode))
		}
		body, err := io.ReadAll(io.LimitReader(res.Body, maxPeerSheet+1))
		if err != nil {
			return nil, err
		}
		if len(body) > maxPeerSheet {
			return nil, errors.New("a sheet too large")
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
		if err != nil || format != "jpeg" || cfg.Width > tp.Columns*tp.Width || cfg.Height > tp.Rows*tp.Height {
			return nil, errors.New("not a sheet of the previews it announced")
		}
		return body, nil
	})
	if err != nil {
		if ctx.Err() == nil {
			l.Logger.Warn("refused a paired server's seek-bar preview sheet", "playback", st.ID, "err", err)
		}
		problem.Send(w, problem.NotFound, "")
		return
	}
	writeSheet(w, data)
}

// maxPlaylistURIs bounds the URIs of a paired server's playlist (MSFP
// media, section 3.1): URI lines and URI= values together.
const maxPlaylistURIs = 50_000

// Tags of the HLS subset (MSFP media, section 3.1), by playlist kind:
// what a relayed playlist keeps; other tags and comments are dropped.
var (
	masterSubset = []string{"#EXTM3U", "#EXT-X-VERSION", "#EXT-X-INDEPENDENT-SEGMENTS", "#EXT-X-STREAM-INF"}
	mediaSubset  = []string{"#EXTM3U", "#EXT-X-VERSION", "#EXT-X-TARGETDURATION", "#EXT-X-MEDIA-SEQUENCE",
		"#EXT-X-PLAYLIST-TYPE", "#EXT-X-INDEPENDENT-SEGMENTS", "#EXTINF", "#EXT-X-DISCONTINUITY", "#EXT-X-ENDLIST", "#EXT-X-MAP"}
)

var errPlaylist = errors.New("a playlist outside what MSFP allows")

// checkRelayedPlaylist checks a paired server's playlist as MSFP media
// section 3.1 says, in its order: at most 2 MiB; lines split at line
// feeds, one carriage return at a line's end removed; a first line of
// exactly #EXTM3U; every other line empty, a tag (or comment) whose every
// URI=, in any case and anywhere, is a quoted file name of the stream, or
// a file name alone; no EXT-X-DEFINE, EXT-X-KEY or EXT-X-SESSION-KEY; at
// most maxPlaylistURIs URIs (rule 14).
func checkRelayedPlaylist(body []byte) error {
	_, err := relayedPlaylist(body, nil)
	return err
}

// relayedPlaylist is a paired server's playlist once checked
// (checkRelayedPlaylist), keeping only the tags of subset, comments
// dropped, each line ending in a line feed; with a nil subset, every
// line is kept.
func relayedPlaylist(body []byte, subset []string) ([]byte, error) {
	if len(body) > maxPlaylist {
		return nil, errPlaylist
	}
	lines := strings.Split(string(body), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSuffix(line, "\r")
	}
	if lines[0] != "#EXTM3U" {
		return nil, errors.New("not a playlist")
	}
	var out strings.Builder
	uris := 0
	for _, line := range lines {
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "#"):
			n, ok := quotedURIs(line)
			if !ok {
				return nil, errors.New("an address that isn't the stream's")
			}
			if line == "#EXT-X-DEFINE" || strings.HasPrefix(line, "#EXT-X-DEFINE:") ||
				strings.HasPrefix(line, "#EXT-X-KEY:") || strings.HasPrefix(line, "#EXT-X-SESSION-KEY:") {
				return nil, errors.New("variables or keys, which MSFP leaves out")
			}
			uris += n
			tag, _, _ := strings.Cut(line, ":")
			if subset != nil && !slices.Contains(subset, tag) {
				continue
			}
		case !fileName(line):
			return nil, errors.New("an address that isn't the stream's")
		default:
			uris++
		}
		if uris > maxPlaylistURIs {
			return nil, errors.New("too many addresses")
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return []byte(out.String()), nil
}

// quotedURIs counts a tag line's URI= values, false when one isn't a
// quoted file name of the stream: every URI=, in any case and anywhere
// in the line (inside another attribute's quoted string too), followed by
// a quote, a file name and a quote. HLS quoted strings have no escapes.
func quotedURIs(line string) (int, bool) {
	// Upper case, ASCII letters only, byte for byte: offsets stay the
	// line's (strings.ToUpper may change a line's length).
	upper := []byte(line)
	for i, c := range upper {
		if 'a' <= c && c <= 'z' {
			upper[i] = c - 'a' + 'A'
		}
	}
	n := 0
	for from := 0; ; {
		i := bytes.Index(upper[from:], []byte("URI="))
		if i < 0 {
			return n, true
		}
		rest := line[from+i+len("URI="):]
		if !strings.HasPrefix(rest, `"`) {
			return n, false
		}
		name, _, closed := strings.Cut(rest[1:], `"`)
		if !closed || !fileName(name) {
			return n, false
		}
		n++
		from += i + len("URI=")
	}
}
