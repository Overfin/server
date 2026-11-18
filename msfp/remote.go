package msfp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/overfin/server/msfp/wire"
)

// Playback, on the viewer's side (MSFP playback sections 2 to 4, video
// section 5, music section 5): this server's people play a peer's title.
// The host decides who may; the engine coarsens what it says of the
// device, calls play, checks and cleans every member of the answer,
// keeps the peer's URLs and ticket to itself, sends progress at its pace
// and stop, and stops after a restart the playbacks left on peers.

// Times of the viewer's side.
const (
	// tellEvery is how often progress is sent while the viewer watches
	// (MSFP playback section 3): never more often, but on a pause or a
	// resume, and every tellEvery while the host reports.
	tellEvery = 45 * time.Second
	// replaceWait is how long a play waits for the stop of the playback
	// it replaces (MSFP playback section 2.3).
	replaceWait = 5 * time.Second
	// stopTries is how often a stop is sent when there's no answer.
	stopTries = 3
)

// PlayRequest is what the host asks of a peer's title for one viewer.
type PlayRequest struct {
	// Title is the key, as the peer listed it.
	Title string
	// Capabilities are what the viewer's device plays, as the host knows
	// it: the engine sends only the values MSFP lists, coarsened (MSFP
	// playback section 2.1).
	Capabilities Capabilities
	// StartMs is where to start; MaxBitrateKbps the viewer's cap (0:
	// none), rounded down to a whole 1,000 from 1,000 up.
	StartMs        int64
	MaxBitrateKbps int
	// The viewer's wishes. Track and version IDs come from a former
	// RemotePlayback of the same title; SubtitleTrack may be "off".
	AudioLanguage, AudioTrack       string
	SubtitleLanguage, SubtitleTrack string
	VersionID                       string
	// Replaces is a RemotePlayback of the same viewer to stop first: to
	// change the audio, the version or the quality, the old playback
	// stops and a new one starts; its stop is waited for up to 5 seconds,
	// so that it doesn't count against the pairing's streams.
	Replaces string
}

// RemotePlayback is a playback on a peer, as the host shows it: its
// answer checked and cleaned. It holds no URL: the host serves the
// media at its own route, and asks the engine for each piece (Fetch).
type RemotePlayback struct {
	// ID is the engine's own for it, never the peer's.
	ID, Peer, Title string
	// Method is direct, remux or transcode; Protocol hls or file, with
	// MimeType for a file.
	Method, Protocol, MimeType string
	PositionMs, DurationMs     int64
	MaxBitrateKbps             int
	Versions                   []Version
	VersionID                  string
	AudioTracks                []Track
	AudioTrack                 string
	// Subtitles, video only. Pictures marks PGS tracks; fetch a track's
	// parts by MediaPath Subtitles or Pictures, each covering
	// SubtitleStretchSeconds.
	SubtitleTracks         []Track
	SubtitleTrack          string
	ForcedTracks           []Track
	ForcedTrack            string
	SubtitleStretchSeconds int
	Segments               []Segment
	Info                   *PlaybackInfo
	// Trickplay is the copy's previews, with the number of sheets to
	// fetch (MediaPath Sheet); nil when the peer sent none, or out of
	// bounds.
	Trickplay *Trickplay
}

// remote is a playback on a peer, live.
type remote struct {
	view      RemotePlayback
	peer      string
	peerName  string
	theirID   string
	base      string // the peer's stream base: its ticket in it, never shown
	audioOnly bool
	subtitles map[string]bool // track ID with a URL: pictures
	ctx       context.Context
	cancel    context.CancelFunc

	// Under remotes.mu. told is when the peer was last sent progress
	// (zero: never), fresh says the host reported since.
	position   int64
	paused     bool
	told       time.Time
	toldPaused bool
	fresh      bool
	playlists  map[string]bool // names a master playlist named
}

// remotes are this server's playbacks on peers.
type remotes struct {
	mu       sync.Mutex
	byID     map[string]*remote
	stopping map[string]int // stops on their way, by peer
	leftover sync.Once
}

// capsWire is a capabilities object as sent.
type capsWire struct {
	Containers       []string `json:"containers,omitempty"`
	AudioCodecs      []string `json:"audioCodecs,omitempty"`
	MaxAudioChannels int      `json:"maxAudioChannels,omitempty"`
	MaxBitrateKbps   int      `json:"maxBitrateKbps,omitempty"`
	VideoCodecs      []string `json:"videoCodecs,omitempty"`
	HDR              []string `json:"hdr,omitempty"`
	MaxHeight        int      `json:"maxHeight,omitempty"`
}

// coarsen is what a peer is told of a device (MSFP playback section 2.1,
// video section 5.1): only the values MSFP lists, lowercase, each once,
// sorted; channels stepped down to 2, 6 or 8, heights to 480, 720, 1080,
// 1440, 2160 or 4320, the bitrate to a whole 1,000 (below, none). nil
// when it says nothing.
func coarsen(c Capabilities, video bool) *capsWire {
	list := func(name string, in []string) []string {
		var out []string
		for _, s := range in {
			s = strings.ToLower(s)
			if slices.Contains(capabilityValues[name], s) && !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
		slices.Sort(out)
		return out
	}
	step := func(v int, steps []int) int {
		if v <= 0 {
			return 0
		}
		out := steps[0]
		for _, s := range steps {
			if v >= s {
				out = s
			}
		}
		return out
	}
	w := &capsWire{Containers: list("containers", c.Containers), AudioCodecs: list("audioCodecs", c.AudioCodecs),
		MaxAudioChannels: step(c.MaxAudioChannels, []int{2, 6, 8}), MaxBitrateKbps: c.MaxBitrateKbps / 1000 * 1000}
	if w.MaxBitrateKbps < 0 || c.MaxBitrateKbps > maxKbps {
		w.MaxBitrateKbps = 0
	}
	if video {
		w.VideoCodecs, w.HDR = list("videoCodecs", c.VideoCodecs), list("hdr", c.HDR)
		w.MaxHeight = wire.StepHeight(c.MaxHeight)
	}
	if w.Containers == nil && w.AudioCodecs == nil && w.VideoCodecs == nil && w.HDR == nil &&
		w.MaxAudioChannels == 0 && w.MaxBitrateKbps == 0 && w.MaxHeight == 0 {
		return nil
	}
	return w
}

// roundKbps is the request's bitrate cap as sent: a whole 1,000 from
// 1,000 up, as it is below (MSFP playback section 2.1).
func roundKbps(kbps int) int {
	switch {
	case kbps <= 0 || kbps > maxKbps:
		return 0
	case kbps < 1000:
		return kbps
	}
	return kbps / 1000 * 1000
}

type playWire struct {
	Title            string    `json:"title"`
	Capabilities     *capsWire `json:"capabilities,omitempty"`
	StartPositionMs  int64     `json:"startPositionMs,omitempty"`
	MaxBitrateKbps   int       `json:"maxBitrateKbps,omitempty"`
	AudioLanguage    string    `json:"audioLanguage,omitempty"`
	AudioTrack       string    `json:"audioTrack,omitempty"`
	VersionID        string    `json:"versionId,omitempty"`
	SubtitleLanguage string    `json:"subtitleLanguage,omitempty"`
	SubtitleTrack    string    `json:"subtitleTrack,omitempty"`
}

// playReading is a play answer as read: every member of its type, or
// the answer fails (MSFP core section 2.3).
type playReading struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Stream *struct {
		URL      string `json:"url"`
		Protocol string `json:"protocol"`
		MimeType string `json:"mimeType"`
	} `json:"stream"`
	PositionMs             int64          `json:"positionMs"`
	DurationMs             int64          `json:"durationMs"`
	Versions               []Version      `json:"versions"`
	VersionID              string         `json:"versionId"`
	AudioTracks            []subtitleWire `json:"audioTracks"`
	AudioTrack             string         `json:"audioTrack"`
	MaxBitrateKbps         int64          `json:"maxBitrateKbps"`
	Info                   *PlaybackInfo  `json:"info"`
	SubtitleTracks         []subtitleWire `json:"subtitleTracks"`
	SubtitleTrack          string         `json:"subtitleTrack"`
	ForcedTracks           []subtitleWire `json:"forcedTracks"`
	ForcedTrack            string         `json:"forcedTrack"`
	SubtitleStretchSeconds int64          `json:"subtitleStretchSeconds"`
	Segments               []Segment      `json:"segments"`
	Trickplay              *trickplayWire `json:"trickplay"`
}

// Play starts a playback of a peer's title for one of this server's
// viewers. The host has decided the viewer may; the engine refuses
// before calling when the peer can't be used, or its last limits say no
// stream. StreamLimitReached, ServerBusy (no answer, none signed, or a
// 503) and BackingOff (a 429, or calls unanswered lately) mean busy now;
// NotPlayable and the others, not playable (MSFP playback section 2.5).
// Errors: ErrNotFound, ErrNotPaired, ErrUncallable, ErrSuspended,
// ErrKeyLost, ErrNotSupported, ErrRefusedDestination,
// ErrCertificateRefused, ErrStreamLimitReached, ErrServerBusy,
// ErrBackingOff, ErrNotPlayable.
func (e *Engine) Play(ctx context.Context, peer string, req PlayRequest) (RemotePlayback, error) {
	p, err := e.peerRecord(ctx, peer)
	if err != nil {
		return RemotePlayback{}, err
	}
	k, ok := wire.ParseKey(req.Title)
	if !ok || !k.Playable {
		return RemotePlayback{}, ErrNotPlayable.with(errors.New("not a key play takes"))
	}
	if err := e.usablePeer(p, wire.PartPlayback); err != nil {
		return RemotePlayback{}, err
	}
	if !e.on(p, wire.PartMedia) || !e.on(p, k.Part) {
		return RemotePlayback{}, ErrNotSupported
	}
	audioOnly := k.Part == wire.PartMusic
	if l := p.TheirLimits; l != nil {
		most := l.Streams
		if audioOnly && l.Audio {
			most = l.AudioStreams
		}
		if most == 0 {
			return RemotePlayback{}, ErrStreamLimitReached
		}
	}
	if req.Replaces != "" {
		if err := e.Stop(ctx, req.Replaces, -1); err != nil && !errors.Is(err, ErrNotFound) {
			e.log.Info("msfp: the playback replaced couldn't be stopped", "error", err)
		}
	}
	e.waitStops(ctx, peer)
	in := playWire{Title: req.Title, Capabilities: coarsen(req.Capabilities, !audioOnly),
		StartPositionMs: min(max(req.StartMs, 0), maxPositionMs), MaxBitrateKbps: roundKbps(req.MaxBitrateKbps)}
	if wire.LanguageTag(req.AudioLanguage) {
		in.AudioLanguage = req.AudioLanguage
	}
	if _, ok := wire.TrackID(req.AudioTrack, 'a'); ok {
		in.AudioTrack = req.AudioTrack
	}
	if wire.ID(req.VersionID) {
		in.VersionID = req.VersionID
	}
	if !audioOnly {
		if wire.LanguageTag(req.SubtitleLanguage) {
			in.SubtitleLanguage = req.SubtitleLanguage
		}
		if _, ok := wire.TrackID(req.SubtitleTrack, 's'); ok || req.SubtitleTrack == "off" {
			in.SubtitleTrack = req.SubtitleTrack
		}
	}
	var raw json.RawMessage
	r, err := e.call(ctx, p, callPlay, in, &raw, false)
	switch {
	case errors.Is(err, ErrUnreachable):
		// No answer, or none signed: busy now (MSFP playback section 2.5).
		return RemotePlayback{}, ErrServerBusy.with(err)
	case err != nil:
		// What lasts says itself (ErrKeyLost, ErrSuspended, ErrUncallable,
		// ErrNotSupported, a destination or certificate refused…), as
		// does ErrBackingOff, busy now.
		return RemotePlayback{}, err
	case r.status == http.StatusConflict && r.problem == string(probNotPlayable):
		return RemotePlayback{}, ErrNotPlayable
	case r.status == http.StatusConflict:
		return RemotePlayback{}, ErrStreamLimitReached
	case r.status == http.StatusServiceUnavailable:
		return RemotePlayback{}, ErrServerBusy
	case r.status == http.StatusTooManyRequests:
		return RemotePlayback{}, ErrBackingOff
	case r.status == http.StatusNotFound && r.problem == string(probNotSupported):
		return RemotePlayback{}, ErrNotSupported
	case r.status != http.StatusOK:
		if r.status == http.StatusNotFound {
			e.helloNow(p.ID)
		}
		return RemotePlayback{}, ErrNotPlayable.with(refusalError(r.status, r.problem))
	}
	rm, why := e.readPlay(p, raw, req.Title, audioOnly)
	if rm == nil {
		e.log.Warn("msfp: a peer's play answer broke MSFP's rules; it is stopped", "peer", p.ID, "why", why)
		var id struct {
			ID string `json:"id"`
		}
		if wire.DecodeEntry(raw, &id) == nil && wire.PlayID(id.ID) {
			e.sendStop(p.ID, id.ID, -1)
		}
		return RemotePlayback{}, ErrNotPlayable.with(errors.New(why))
	}
	idBytes, err := e.random(16)
	if err != nil {
		e.sendStop(p.ID, rm.theirID, -1)
		return RemotePlayback{}, err
	}
	rm.view.ID = "r" + lowerBase32.EncodeToString(idBytes)
	rm.ctx, rm.cancel = context.WithCancel(context.Background())
	rm.position = rm.view.PositionMs
	// Kept until stopped, so that after a restart the peer is told it's
	// over (MSFP playback section 6.3).
	if err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		return tx.PutLeftover(Leftover{Peer: p.ID, PlaybackID: rm.theirID})
	}); err != nil {
		e.log.Warn("msfp: a playback on a peer couldn't be noted", "error", err)
	}
	rs := &e.remotes
	rs.mu.Lock()
	if rs.byID == nil {
		rs.byID = map[string]*remote{}
	}
	rs.byID[rm.view.ID] = rm
	rs.mu.Unlock()
	return rm.view, nil
}

// readPlay checks and cleans a peer's play answer (MSFP playback section
// 2.3, video sections 5.3 and 5.5, music section 5): nil, and why, when
// it breaks a "must".
func (e *Engine) readPlay(p *PeerRecord, raw json.RawMessage, title string, audioOnly bool) (*remote, string) {
	var members map[string]json.RawMessage
	if json.Unmarshal(raw, &members) != nil {
		return nil, "not an object"
	}
	// title and next are dropped, whatever they hold.
	delete(members, "title")
	delete(members, "next")
	if audioOnly {
		// A track's answer has no subtitles, no skip markers, no previews.
		for _, m := range []string{"subtitleTracks", "subtitleTrack", "forcedTracks", "forcedTrack",
			"subtitleStretchSeconds", "segments", "trickplay"} {
			delete(members, m)
		}
	}
	without, err := json.Marshal(members)
	if err != nil {
		return nil, "not an object"
	}
	var a playReading
	if err := wire.DecodeAnswer(without, &a); err != nil {
		return nil, "a member of the wrong type"
	}
	switch {
	case !wire.PlayID(a.ID):
		return nil, "an id of another form"
	case a.Method != "direct" && a.Method != "remux" && a.Method != "transcode":
		return nil, "a method MSFP doesn't have"
	case a.Stream == nil:
		return nil, "no stream"
	}
	prefix := strings.TrimSuffix(cmp.Or(p.Base, defaultBase), "/api/v1/federation/")
	root := p.Address + prefix + streamRoute
	base, ok := wire.StreamBase(a.Stream.URL, root, a.Stream.Protocol)
	if !ok {
		return nil, "a stream URL not under its stream root"
	}
	rm := &remote{peer: p.ID, peerName: p.Name, theirID: a.ID, base: base, audioOnly: audioOnly, subtitles: map[string]bool{}}
	v := &rm.view
	v.Peer, v.Title, v.Method, v.Protocol = p.ID, title, a.Method, a.Stream.Protocol
	if v.Protocol == "file" {
		v.MimeType = wire.RelayedMediaType(a.Stream.MimeType)
	}
	v.PositionMs = min(max(a.PositionMs, 0), maxPositionMs)
	if a.DurationMs > 0 && a.DurationMs <= maxPositionMs {
		v.DurationMs = a.DurationMs
	}
	if a.MaxBitrateKbps > 0 && a.MaxBitrateKbps <= maxKbps {
		v.MaxBitrateKbps = int(a.MaxBitrateKbps)
	}
	for _, ver := range a.Versions {
		if n := wire.CleanText(ver.Name, 64); wire.ID(ver.ID) && n != "" {
			v.Versions = append(v.Versions, Version{ID: ver.ID, Name: n})
		}
	}
	keepID := func(id string) string {
		if wire.ID(id) || id == "off" {
			return id
		}
		return ""
	}
	v.VersionID = keepID(a.VersionID)
	v.AudioTracks = cleanTracks(plainTracks(a.AudioTracks), 'a')
	v.AudioTrack = keepID(a.AudioTrack)
	v.Info = cleanInfo(a.Info)
	if !audioOnly {
		for _, list := range [][]subtitleWire{a.SubtitleTracks, a.ForcedTracks} {
			for _, t := range list {
				if t.URL != "" && t.URL != wire.SubtitleURL(base, t.ID, t.Pictures) {
					return nil, "a subtitle URL that isn't its track's"
				}
				if t.URL != "" {
					rm.subtitles[t.ID] = t.Pictures
				}
			}
		}
		v.SubtitleTracks = cleanTracks(plainTracks(a.SubtitleTracks), 's')
		v.ForcedTracks = cleanTracks(plainTracks(a.ForcedTracks), 's')
		v.SubtitleTrack, v.ForcedTrack = keepID(a.SubtitleTrack), keepID(a.ForcedTrack)
		if len(v.SubtitleTracks)+len(v.ForcedTracks) > 0 {
			v.SubtitleStretchSeconds = wire.StretchSeconds
		}
		v.Segments = cleanSegments(a.Segments)
		if tp := a.Trickplay; tp != nil {
			for n, u := range tp.Sheets {
				if u != wire.SheetURL(base, n) {
					return nil, "a sheet URL that isn't its own"
				}
			}
			t := wire.Trickplay{TileWidth: tp.TileWidth, TileHeight: tp.TileHeight, Columns: tp.Columns, Rows: tp.Rows,
				IntervalMs: tp.IntervalMs, TileCount: tp.TileCount, Sheets: len(tp.Sheets)}
			if t.InBounds() {
				v.Trickplay = &Trickplay{TileWidth: t.TileWidth, TileHeight: t.TileHeight, Columns: t.Columns,
					Rows: t.Rows, IntervalMs: t.IntervalMs, TileCount: t.TileCount, Sheets: t.Sheets}
			}
		}
	}
	return rm, ""
}

func plainTracks(in []subtitleWire) []Track {
	out := make([]Track, len(in))
	for i, t := range in {
		out[i] = t.Track
	}
	return out
}

// liveRemote is a playback on a peer by the engine's ID; nil for none.
func (e *Engine) liveRemote(id string) *remote {
	rs := &e.remotes
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.byID[id]
}

// Progress notes where a viewer is in a playback on a peer (MSFP
// playback section 3): the peer is told at the first report, at once on
// a pause or a resume, and otherwise every 45 seconds at most while the
// host reports, by Run's work of each second when no report comes at the
// time. It returns at once. A 410 from the peer ends the playback
// (RemotePlaybackEnded).
func (e *Engine) Progress(id string, positionMs int64, paused bool) {
	rs := &e.remotes
	rs.mu.Lock()
	rm := rs.byID[id]
	if rm == nil {
		rs.mu.Unlock()
		return
	}
	now := e.now()
	rm.position, rm.paused, rm.fresh = min(max(positionMs, 0), maxPositionMs), paused, true
	due := rm.told.IsZero() || paused != rm.toldPaused || now.Sub(rm.told) >= tellEvery
	var in progressCall
	if due {
		in = rm.tell(now)
	}
	rs.mu.Unlock()
	if due {
		e.sendProgress(rm, in)
	}
}

// progressCall is progress's request.
type progressCall struct {
	ID         string `json:"id"`
	PositionMs int64  `json:"positionMs"`
	Paused     bool   `json:"paused,omitempty"`
}

// tell notes that the peer is told now where the viewer is, and gives
// what it is sent. Under remotes.mu.
func (rm *remote) tell(now time.Time) progressCall {
	rm.told, rm.toldPaused, rm.fresh = now, rm.paused, false
	return progressCall{ID: rm.theirID, PositionMs: rm.position, Paused: rm.paused}
}

// tellWatched sends progress for the playbacks on peers the host
// reported since they were last sent it, 45 seconds ago or more: a host
// whose reports don't fall on the time still keeps its playbacks there
// told every 45 seconds.
func (e *Engine) tellWatched() {
	now := e.now()
	rs := &e.remotes
	rs.mu.Lock()
	type due struct {
		rm *remote
		in progressCall
	}
	var send []due
	for _, rm := range rs.byID {
		if rm.fresh && now.Sub(rm.told) >= tellEvery {
			send = append(send, due{rm, rm.tell(now)})
		}
	}
	rs.mu.Unlock()
	for _, d := range send {
		e.sendProgress(d.rm, d.in)
	}
}

// sendProgress sends progress in the background; a 410 ends the
// playback.
func (e *Engine) sendProgress(rm *remote, in progressCall) {
	e.background(func(ctx context.Context) {
		p, err := e.peerRecord(ctx, rm.peer)
		if err != nil {
			e.endRemote(rm, EndPairingEnded, true)
			return
		}
		var out struct{}
		r, err := e.call(ctx, p, callProgress, in, &out, true)
		switch {
		case err == nil && r.status == http.StatusGone:
			e.endRemote(rm, EndExpired, true)
			e.forgetLeftover(ctx, rm)
		case err != nil && !errors.Is(err, ErrBackingOff):
			e.log.Info("msfp: a peer couldn't be told a playback is watched", "peer", rm.peer, "error", err)
		}
	})
}

// Stop ends a playback on a peer: the peer is sent stop (again when it
// doesn't answer, a few times), and the playback is forgotten.
// positionMs is where the viewer stopped, -1 when not known. Error:
// ErrNotFound.
func (e *Engine) Stop(ctx context.Context, id string, positionMs int64) error {
	rm := e.liveRemote(id)
	if rm == nil {
		return ErrNotFound
	}
	if positionMs < 0 {
		rs := &e.remotes
		rs.mu.Lock()
		positionMs = rm.position
		rs.mu.Unlock()
	}
	e.endRemote(rm, EndStopped, false)
	done := e.stopping(rm.peer)
	defer done()
	e.stopOnPeer(ctx, rm.peer, rm.theirID, positionMs)
	e.forgetLeftover(ctx, rm)
	return nil
}

// sendStop sends stop in the background.
func (e *Engine) sendStop(peer, theirID string, positionMs int64) {
	done := e.stopping(peer)
	e.background(func(ctx context.Context) {
		defer done()
		e.stopOnPeer(ctx, peer, theirID, positionMs)
	})
}

// stopOnPeer sends stop, tried again a few times without an answer; not
// at all to a peer that can't be used now.
func (e *Engine) stopOnPeer(ctx context.Context, peer, theirID string, positionMs int64) {
	p, err := e.peerRecord(ctx, peer)
	if err != nil || e.usablePeer(p, wire.PartPlayback) != nil {
		return
	}
	in := struct {
		ID         string `json:"id"`
		PositionMs int64  `json:"positionMs,omitempty"`
	}{theirID, max(positionMs, 0)}
	for try := range stopTries {
		var out struct{}
		r, err := e.call(ctx, p, callStop, in, &out, false)
		if err == nil || !errors.Is(err, ErrUnreachable) || ctx.Err() != nil {
			if err == nil && r.status != http.StatusOK {
				e.log.Info("msfp: a peer refused a stop", "peer", peer, "status", r.status)
			}
			return
		}
		if try < stopTries-1 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second << try):
			}
		}
	}
}

// stopping notes a stop on its way to peer, until done.
func (e *Engine) stopping(peer string) (done func()) {
	rs := &e.remotes
	rs.mu.Lock()
	if rs.stopping == nil {
		rs.stopping = map[string]int{}
	}
	rs.stopping[peer]++
	rs.mu.Unlock()
	return sync.OnceFunc(func() {
		rs.mu.Lock()
		if rs.stopping[peer]--; rs.stopping[peer] <= 0 {
			delete(rs.stopping, peer)
		}
		rs.mu.Unlock()
	})
}

// waitStops waits up to 5 seconds for the stops on their way to peer.
func (e *Engine) waitStops(ctx context.Context, peer string) {
	deadline := time.Now().Add(replaceWait)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		rs := &e.remotes
		rs.mu.Lock()
		n := rs.stopping[peer]
		rs.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// endRemote ends a playback on a peer here: its fetches are cut, and,
// when the peer or the pairing ended it (tell), the host is told.
func (e *Engine) endRemote(rm *remote, why EndReason, tell bool) {
	rs := &e.remotes
	rs.mu.Lock()
	if rs.byID[rm.view.ID] != rm {
		rs.mu.Unlock()
		return
	}
	delete(rs.byID, rm.view.ID)
	rs.mu.Unlock()
	rm.cancel()
	if tell {
		e.notify(RemotePlaybackEnded{Peer: rm.peer, Name: rm.peerName, Playback: rm.view.ID, Reason: why})
	}
}

// endRemotes ends this server's playbacks on a peer, as the pairing
// ended, or it was suspended, or can't be called.
func (e *Engine) endRemotes(peer string, why EndReason) {
	rs := &e.remotes
	rs.mu.Lock()
	var end []*remote
	for _, rm := range rs.byID {
		if rm.peer == peer {
			end = append(end, rm)
		}
	}
	rs.mu.Unlock()
	for _, rm := range end {
		e.endRemote(rm, why, true)
	}
}

func (e *Engine) forgetLeftover(ctx context.Context, rm *remote) {
	if err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		return tx.DeleteLeftover(Leftover{Peer: rm.peer, PlaybackID: rm.theirID})
	}); err != nil {
		e.log.Warn("msfp: a playback on a peer couldn't be forgotten", "error", err)
	}
}

// stopLeftovers tells peers the playbacks this server had on them before
// it started are over (MSFP playback section 6.3): once, at the first
// run.
func (e *Engine) stopLeftovers(ctx context.Context) {
	e.remotes.leftover.Do(func() {
		var left []Leftover
		if err := e.cfg.Store.Read(ctx, func(tx Tx) error {
			var err error
			left, err = tx.Leftovers()
			return err
		}); err != nil {
			e.log.Warn("msfp: the playbacks left on peers couldn't be read", "error", err)
			return
		}
		live := map[Leftover]bool{}
		e.remotes.mu.Lock()
		for _, rm := range e.remotes.byID {
			live[Leftover{Peer: rm.peer, PlaybackID: rm.theirID}] = true
		}
		e.remotes.mu.Unlock()
		for _, l := range left {
			if live[l] {
				continue // started since this run began: not left over
			}
			e.stopOnPeer(ctx, l.Peer, l.PlaybackID, -1)
			if err := e.cfg.Store.Update(ctx, func(tx Tx) error { return tx.DeleteLeftover(l) }); err != nil {
				e.log.Warn("msfp: a playback left on a peer couldn't be forgotten", "error", err)
			}
		}
	})
}
