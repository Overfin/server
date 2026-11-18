package msfp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/overfin/server/msfp/wire"
)

// Media, on the viewer's side (MSFP media sections 2 and 3, video section
// 6): the relay. The host's viewer route maps its own URL to a MediaPath
// and calls Fetch; the engine fetches from the peer's stream root with
// the playback's ticket, which never leaves the engine, and checks
// everything before a byte reaches the viewer.

// Bounds of the relay (MSFP media section 2.4).
const (
	// smallReadTime bounds the reading whole of a playlist, a subtitle
	// part, a PGS part or a sheet.
	smallReadTime = 2 * time.Minute
	// smallReads and sheetReads bound how many of them are held in memory
	// at once across the server.
	smallReads = 4
	sheetReads = 4
)

// Fetch serves a piece of a playback on a peer, for the viewer playing
// it: the same MediaPath the owning side serves; rng is the viewer's
// Range header as received ("" for none), sent on for the file and
// segments only, once well-formed. Content-Type is set by the engine. A
// fetch is cut when its playback ends. Errors (nothing written then):
// ErrNotFound (any failed fetch: the peer's body and headers never pass
// with a failure), ErrRangeNotSatisfiable (a malformed Range: nothing is
// fetched; or the peer's 416), ErrServerBusy (the bounds of fetches at
// once, or the peer's 503).
func (e *Engine) Fetch(ctx context.Context, id string, path MediaPath, rng string, out ResponseSink) error {
	rm := e.liveRemote(id)
	if rm == nil {
		return ErrNotFound
	}
	if path.Kind == MediaSegment && e.isPlaylist(rm, path.Name) {
		path.Kind = MediaPlaylist
	}
	name, ok := e.relayName(rm, path)
	if !ok {
		return ErrNotFound
	}
	media := path.Kind == MediaFile || path.Kind == MediaSegment
	var want Range
	if !media {
		rng = ""
	} else if rng != "" {
		if want, ok = ParseRange(rng); !ok {
			return ErrRangeNotSatisfiable
		}
	}
	p, err := e.peerRecord(ctx, rm.peer)
	if err != nil || e.usablePeer(p, wire.PartMedia) != nil {
		// Never to a peer that can't be called, or is suspended (MSFP core
		// sections 7.5 and 12.6), whatever ended its playbacks.
		if err == nil || errors.Is(err, ErrNotFound) {
			e.endRemote(rm, EndPairingEnded, true)
		}
		return ErrNotFound
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(rm.ctx, cancel)
	defer stop()
	if !media {
		var cancelSmall context.CancelFunc
		ctx, cancelSmall = context.WithTimeout(ctx, smallReadTime)
		defer cancelSmall()
	}
	freeOne, ok := e.relaySlots.take(ctx, rm.view.ID)
	if !ok {
		return ErrServerBusy
	}
	defer freeOne()
	freeAll, ok := e.peerRelaySlots.take(ctx, rm.peer)
	if !ok {
		return ErrServerBusy
	}
	defer freeAll()
	if !media {
		gate := e.smallGate
		if path.Kind == MediaSheet {
			gate = e.sheetGate
		}
		select {
		case gate <- struct{}{}:
			defer func() { <-gate }()
		case <-ctx.Done():
			return ErrServerBusy
		}
	}
	res, err := e.relayGet(ctx, p, rm.base+name, rng)
	if err != nil {
		if ctx.Err() == nil {
			e.log.Info("msfp: a peer's media couldn't be fetched", "peer", rm.peer, "error", err)
		}
		return ErrNotFound
	}
	defer res.Body.Close()
	if ce := res.Header.Get("Content-Encoding"); res.Uncompressed || ce != "" && !strings.EqualFold(ce, "identity") {
		return ErrNotFound
	}
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusPartialContent:
		if rng == "" {
			return ErrNotFound
		}
	case http.StatusRequestedRangeNotSatisfiable:
		if rng != "" {
			return ErrRangeNotSatisfiable
		}
		return ErrNotFound
	case http.StatusServiceUnavailable:
		return ErrServerBusy
	default:
		return ErrNotFound
	}
	if media {
		return e.relayMedia(rm, res, rng, want, out)
	}
	return e.relaySmall(rm, path, res, out)
}

// relayName is the name fetched under the peer's stream base for path;
// false for what the playback doesn't have.
func (e *Engine) relayName(rm *remote, path MediaPath) (string, bool) {
	v := rm.view
	switch path.Kind {
	case MediaFile:
		return "file", v.Protocol == "file"
	case MediaMaster:
		return "master.m3u8", v.Protocol == "hls"
	case MediaPlaylist, MediaSegment:
		return path.Name, v.Protocol == "hls" && wire.FileName(path.Name) && path.Name != "master.m3u8"
	case MediaSubtitles, MediaPictures:
		pictures, ok := rm.subtitles[path.Track]
		if !ok || pictures != (path.Kind == MediaPictures) || path.Part < 0 || path.Part > 9999 {
			return "", false
		}
		return wire.SubtitleURL("", path.Track, pictures) + "/" + strconv.Itoa(path.Part), true
	case MediaSheet:
		if v.Trickplay == nil || path.Sheet < 0 || path.Sheet >= v.Trickplay.Sheets {
			return "", false
		}
		return wire.SheetURL("", path.Sheet), true
	}
	return "", false
}

// relayGet fetches a URL of a peer's stream: a plain GET, nothing but
// Range, over a connection that needs a trusted or named certificate;
// refused for its certificate, a hello, then once more (MSFP media
// section 2). The URL holds the ticket: it is never logged.
func (e *Engine) relayGet(ctx context.Context, p *PeerRecord, url, rng string) (*http.Response, error) {
	get := func(p *PeerRecord) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, errors.New("a URL that isn't one")
		}
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		res, err := e.conns.mediaClient(p).Do(req)
		if err != nil {
			return nil, unwrapURL(err)
		}
		return res, nil
	}
	res, err := get(p)
	if err != nil && errors.Is(err, wire.ErrUntrustedCertificate) && ctx.Err() == nil {
		if _, herr := e.check1(ctx, p.ID, false); herr == nil {
			if q, qerr := e.peerRecord(ctx, p.ID); qerr == nil && e.usablePeer(q, wire.PartMedia) == nil {
				res, err = get(q)
			}
		}
	}
	return res, err
}

var contentRangeRe = regexp.MustCompile(`^bytes ([0-9]{1,15})-([0-9]{1,15})/([0-9]{1,15}|\*)$`)

// partialLength says whether a 206 answers the range asked (MSFP media
// section 2.1), and gives its length and its Content-Range.
func partialLength(h http.Header, want Range) (int64, string, bool) {
	ranges := h.Values("Content-Range")
	if len(ranges) != 1 {
		return 0, "", false
	}
	m := contentRangeRe.FindStringSubmatch(ranges[0])
	if m == nil {
		return 0, "", false
	}
	start, _ := strconv.ParseInt(m[1], 10, 64)
	end, _ := strconv.ParseInt(m[2], 10, 64)
	total := int64(-1)
	if m[3] != "*" {
		total, _ = strconv.ParseInt(m[3], 10, 64)
	}
	if start > end || total >= 0 && end >= total {
		return 0, "", false
	}
	switch {
	case want.Suffix > 0:
		if total < 0 || start != max(total-want.Suffix, 0) {
			return 0, "", false
		}
	case start != want.First || want.Last >= 0 && end > want.Last:
		return 0, "", false
	}
	n := end - start + 1
	if cl := h.Values("Content-Length"); len(cl) > 1 || len(cl) == 1 && cl[0] != strconv.FormatInt(n, 10) {
		return 0, "", false
	}
	return n, ranges[0], true
}

// relayMedia relays the file or a segment: a 206 only for the range
// asked, never more than its length, never a playlist under a media name,
// its type kept only when it is a plain video/ or audio/ one.
func (e *Engine) relayMedia(rm *remote, res *http.Response, rng string, want Range, out ResponseSink) error {
	length, contentRange := res.ContentLength, ""
	if res.StatusCode == http.StatusPartialContent {
		n, cr, ok := partialLength(res.Header, want)
		if !ok {
			e.log.Info("msfp: a peer's part of a stream wasn't the range asked", "peer", rm.peer)
			return ErrNotFound
		}
		length, contentRange = n, cr
	} else if cl := res.Header.Values("Content-Length"); len(cl) > 1 {
		return ErrNotFound
	}
	body := bufio.NewReaderSize(res.Body, 512)
	if start, _ := body.Peek(len("#EXTM3U")); string(start) == "#EXTM3U" {
		// Players read a playlist by what it holds: none passes unchecked.
		e.log.Info("msfp: a peer sent a playlist under a media name", "peer", rm.peer)
		return ErrNotFound
	}
	var r io.Reader = body
	if length >= 0 {
		r = io.LimitReader(body, length)
	}
	out.Start(res.StatusCode, wire.RelayedMediaType(res.Header.Get("Content-Type")), length, contentRange)
	_, err := io.Copy(out, r)
	if err != nil && rm.ctx.Err() == nil {
		return fmt.Errorf("msfp: a relayed stream was cut: %w", err)
	}
	return nil
}

// relaySmall relays what is read whole: playlists checked and kept to
// the subset, WebVTT rebuilt, PGS checked, sheets checked and re-encoded
// (MSFP media section 3.1, video sections 6.2 to 6.4).
func (e *Engine) relaySmall(rm *remote, path MediaPath, res *http.Response, out ResponseSink) error {
	limit := int64(wire.MaxPlaylist)
	playlist := path.Kind == MediaMaster || path.Kind == MediaPlaylist
	rs := &e.remotes
	switch path.Kind {
	case MediaSubtitles:
		limit = wire.MaxWebVTT
	case MediaPictures:
		limit = wire.MaxPGS
	case MediaSheet:
		limit = wire.MaxSheetBytes
	}
	if res.ContentLength > limit {
		return ErrNotFound
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		return ErrNotFound
	}
	contentType := "application/vnd.apple.mpegurl"
	switch {
	case playlist:
		subset := wire.MediaSubset
		if path.Kind == MediaMaster {
			subset = wire.MasterSubset
		}
		kept, err := wire.FilterPlaylist(body, subset)
		if err != nil {
			e.log.Info("msfp: a peer's playlist was refused", "peer", rm.peer, "error", err)
			return ErrNotFound
		}
		body = kept
		if path.Kind == MediaMaster {
			// What it names is fetched as a playlist, whatever its name.
			rs.mu.Lock()
			if rm.playlists == nil {
				rm.playlists = map[string]bool{}
			}
			for _, n := range wire.URILines(kept) {
				rm.playlists[n] = true
			}
			rs.mu.Unlock()
		}
	case path.Kind == MediaSubtitles:
		vtt, ok := wire.RebuildWebVTT(body)
		if !ok {
			return ErrNotFound
		}
		body, contentType = vtt, "text/vtt; charset=utf-8"
	case path.Kind == MediaPictures:
		// An empty part is a stretch with no subtitles: relayed as such.
		if wire.CheckPGS(body) != nil {
			return ErrNotFound
		}
		contentType = "application/octet-stream"
	case path.Kind == MediaSheet:
		tp := rm.view.Trickplay
		w, h := tp.Columns*tp.TileWidth, tp.Rows*tp.TileHeight
		if !sheetFits(body, w, h) {
			return ErrNotFound
		}
		again, err := e.cfg.SheetEncoder.Reencode(body, w, h)
		if err != nil {
			return ErrNotFound
		}
		body, contentType = again, "image/jpeg"
	}
	out.Start(http.StatusOK, contentType, int64(len(body)), "")
	_, err = out.Write(body)
	return err
}

// isPlaylist says whether a name of a playback was named by its master
// playlist, or ends in .m3u8: fetched and checked as a playlist.
func (e *Engine) isPlaylist(rm *remote, name string) bool {
	rs := &e.remotes
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rm.playlists[name] || strings.HasSuffix(name, ".m3u8")
}
