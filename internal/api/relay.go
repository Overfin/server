package api

import (
	"bufio"
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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/overfin/server/internal/federation"
	"github.com/overfin/server/internal/outbound"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/slots"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/msfp/wire"
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
	// Never asked compressed: Go would decompress on the fly and drop
	// Content-Length, the bound of what is read (MSFP media, section 2.4).
	DisableCompression: true,
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

// Bounds of what a paired server's playbacks fetch at once (MSFP media,
// section 2.4), on this server as the viewer's: per playback (a player
// asks one segment at a time, or holds one range of the file open, and
// opens another when it seeks) and per paired server. Over them, a fetch
// waits a moment in a short queue, or is refused with 503.
var (
	relayPerPlayback = &slots.Slots{Size: 8, Queue: 8, Wait: 10 * time.Second}
	relayPerPeer     = &slots.Slots{Size: 64, Queue: 64, Wait: 10 * time.Second}
)

// smallReadTime bounds the time a playlist, a subtitle part or a sheet
// takes to read whole, from either side's upstream (MSFP media, section
// 2.4). The file and segments last as long as their playback.
var smallReadTime = 2 * time.Minute

// relaySlots takes a fetch's slots, for its playback and its paired
// server, and gives the function that frees them; false when it couldn't.
func relaySlots(ctx context.Context, st *playback.Stream) (func(), bool) {
	one, ok := relayPerPlayback.Take(ctx, st.ID)
	if !ok {
		return nil, false
	}
	all, ok := relayPerPeer.Take(ctx, st.Remote.PeerID)
	if !ok {
		one()
		return nil, false
	}
	return func() { all(); one() }, true
}

// relay answers a browser's request for a paired server's stream with
// what that server gives, checked as anything from Jellyfin is: playlists
// within MSFP's subset, subtitles sanitized, media passed on with the
// headers streaming needs, a 206 only for the range asked (MSFP media,
// section 2). A failed fetch is a 404 to the browser, never the paired
// server's body; its 416 and 503 are passed on as such.
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
	var br byteRange
	if rh != "" && media {
		var ok bool
		if br, ok = parseRange(rh); !ok {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
	} else {
		rh = ""
	}
	if !media {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, smallReadTime)
		defer stop()
	}
	free, ok := relaySlots(ctx, st)
	if !ok {
		if ctx.Err() == nil {
			problem.Send(w, problem.ServerBusy, "")
		}
		return
	}
	defer free()
	// What is read whole takes one of the server's few slots for it
	// (taken after this playback's, which may wait), freed once checked,
	// before a slow viewer reads it.
	freeSmall := func() {}
	if !media {
		// Within the small read's time, not the request's: waiting, it
		// holds this playback's relay slots.
		f, ok := subtitleSlotWithin(ctx)
		if !ok {
			if r.Context().Err() == nil {
				problem.Send(w, problem.ServerBusy, "")
			}
			return
		}
		freeSmall = sync.OnceFunc(f)
		defer freeSmall()
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
	if ce := res.Header.Get("Content-Encoding"); res.Uncompressed || ce != "" && !strings.EqualFold(ce, "identity") {
		// Asked for none: what is sent is the bytes themselves.
		l.Logger.Warn("refused a paired server's stream: encoded", "playback", st.ID)
		problem.Send(w, problem.NotFound, "")
		return
	}
	switch res.StatusCode {
	case http.StatusOK, http.StatusPartialContent:
	case http.StatusRequestedRangeNotSatisfiable:
		if rh != "" {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		problem.Send(w, problem.NotFound, "")
		return
	case http.StatusServiceUnavailable:
		problem.Send(w, problem.ServerBusy, "")
		return
	default:
		problem.Send(w, problem.NotFound, "")
		return
	}
	// What the paired server says it sends, and what is read of it: a
	// 206 only for the range asked, and never more than its length.
	length := res.ContentLength
	if res.StatusCode == http.StatusPartialContent {
		n, ok := checkPartial(rh, br, res.Header)
		if !ok {
			l.Logger.Warn("refused a paired server's part of a stream: not the range asked", "playback", st.ID)
			problem.Send(w, problem.NotFound, "")
			return
		}
		length = n
	}
	if !media {
		limit := int64(wire.MaxPlaylist)
		switch {
		case what.vtt:
			limit = wire.MaxWebVTT
		case what.pgs:
			limit = wire.MaxPGS
		}
		body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
		if err != nil || int64(len(body)) > limit {
			problem.Send(w, problem.NotFound, "")
			return
		}
		contentType := "application/vnd.apple.mpegurl"
		switch {
		case what.playlist:
			subset := wire.MediaSubset
			if what.path == "master.m3u8" {
				subset = wire.MasterSubset
			}
			kept, err := wire.FilterPlaylist(body, subset)
			if err != nil {
				l.Logger.Warn("refused a paired server's playlist", "playback", st.ID, "err", err)
				problem.Send(w, problem.NotFound, "")
				return
			}
			body = kept
			if what.path == "master.m3u8" {
				// What it names is fetched as a playlist, and checked as
				// one, whatever its name (players read it as one).
				rm.NamePlaylists(wire.URILines(kept))
			}
		case what.vtt:
			vtt, ok := wire.RebuildWebVTT(body)
			if !ok {
				problem.Send(w, problem.NotFound, "")
				return
			}
			body, contentType = vtt, "text/vtt; charset=utf-8"
		case what.pgs:
			if wire.CheckPGS(body) != nil {
				problem.Send(w, problem.NotFound, "")
				return
			}
			contentType = "application/octet-stream"
		}
		freeSmall()
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
		return
	}
	// A playlist under a media name (a master playlist may name its
	// variants anything) is never relayed unchecked: players read a
	// playlist by what it holds.
	peek := bufio.NewReaderSize(res.Body, 512)
	if start, _ := peek.Peek(len("#EXTM3U")); string(start) == "#EXTM3U" {
		l.Logger.Warn("refused a paired server's stream: a playlist under a media name", "playback", st.ID)
		problem.Send(w, problem.NotFound, "")
		return
	}
	h := w.Header()
	h.Set("Content-Type", mediaType(res.Header.Get("Content-Type")))
	keep := []string{"Content-Length", "Accept-Ranges"}
	if res.StatusCode == http.StatusPartialContent {
		keep = append(keep, "Content-Range") // checked above; a 200 is the whole
	}
	for _, k := range keep {
		if v := res.Header.Get(k); v != "" && len(v) < 200 {
			h.Set(k, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	if r.Method == http.MethodHead {
		return // the headers only: nothing more read from the paired server
	}
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
	body := io.Reader(peek)
	if length >= 0 {
		body = io.LimitReader(peek, length)
	}
	_, _ = io.Copy(w, body)
}

// byteRange is a Range header of one of MSFP media section 2.1's three
// forms: bytes=first-last, bytes=first- (last -1), bytes=-count
// (suffix, first and last unused).
type byteRange struct {
	first, last int64
	suffix      int64
}

// parseRange reads a Range header of MSFP media section 2.1: one range of
// bytes, each number 1 to 15 digits; never several ranges, "bytes=-",
// "bytes=-0", or a first byte past the last.
func parseRange(h string) (byteRange, bool) {
	if !rangeRe.MatchString(h) {
		return byteRange{}, false
	}
	from, to, _ := strings.Cut(strings.TrimPrefix(h, "bytes="), "-")
	a, errA := strconv.ParseInt(from, 10, 64)
	b, errB := strconv.ParseInt(to, 10, 64)
	switch {
	case from == "" && to == "":
		return byteRange{}, false
	case from == "":
		return byteRange{suffix: b}, errB == nil && b >= 1
	case to == "":
		return byteRange{first: a, last: -1}, errA == nil
	}
	return byteRange{first: a, last: b}, errA == nil && errB == nil && a <= b
}

var contentRangeRe = regexp.MustCompile(`^bytes (\d{1,15})-(\d{1,15})/(\d{1,15}|\*)$`)

// checkPartial says whether a 206's headers answer the range asked
// (MSFP media, section 2.1), and gives its length: Content-Range
// "bytes start-end/total" (total "*" when unknown) with start ≤ end <
// total, start the range's first byte (total − count, or 0, for the last
// count bytes), end at most its last; Content-Length, when given, end −
// start + 1. A 206 to a request without a range never does.
func checkPartial(rangeHeader string, br byteRange, h http.Header) (int64, bool) {
	m := contentRangeRe.FindStringSubmatch(h.Get("Content-Range"))
	if rangeHeader == "" || m == nil || len(h.Values("Content-Range")) != 1 {
		return 0, false
	}
	start, _ := strconv.ParseInt(m[1], 10, 64)
	end, _ := strconv.ParseInt(m[2], 10, 64)
	total := int64(-1)
	if m[3] != "*" {
		total, _ = strconv.ParseInt(m[3], 10, 64)
	}
	if start > end || (total >= 0 && end >= total) {
		return 0, false
	}
	switch {
	case br.suffix > 0:
		if total < 0 || start != max(total-br.suffix, 0) {
			return 0, false
		}
	case start != br.first || (br.last >= 0 && end > br.last):
		return 0, false
	}
	n := end - start + 1
	if cl := h.Values("Content-Length"); len(cl) > 1 || (len(cl) == 1 && cl[0] != strconv.FormatInt(n, 10)) {
		return 0, false
	}
	return n, true
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
	// Never to a server whose key is revoked, or that can't be called
	// (MSFP core sections 12.6 and 7.5), whatever ends its playbacks.
	if !peer.Usable() || !peer.Callable() {
		return nil, problem.NotFound, errNoRelay
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
		ctx, stop := context.WithTimeout(ctx, smallReadTime)
		defer stop()
		free, ok := relaySlots(ctx, st)
		if !ok {
			return nil, errors.New("too many fetches of this playback at once")
		}
		defer free()
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
