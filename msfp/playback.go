package msfp

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/overfin/server/msfp/wire"
)

// Playback, on the owning side (MSFP playback sections 2 to 6, video
// section 5, music section 5): play, progress and stop from peers, the
// choice of the copy and the tracks, the pairing's limits counted in one
// step with the host's start (Admit), the playbacks' states, tickets and
// endings. Playbacks live in memory: after a restart, a peer's progress
// gets 410 and it starts again.

// Bounds of play's members (MSFP playback section 2.1).
const (
	maxPositionMs = 8_640_000_000 // 100 days
	maxKbps       = 10_000_000
	defaultKbps   = 120_000
)

// Times of a playback (MSFP playback section 6.3).
const (
	// expireAfter: a playback with neither progress nor a media request
	// for this long ends.
	expireAfter = 2 * time.Minute
	// expiryEvery is how often expired playbacks are looked for.
	expiryEvery = 30 * time.Second
	// endedKept is how long why a playback ended is remembered.
	endedKept = time.Hour
	// hostTime bounds the host's Stop and Progress calls made in the
	// background.
	hostTime = 30 * time.Second
)

// plays are the peers' playbacks here, live, and those ended lately.
type plays struct {
	mu       sync.Mutex
	byID     map[string]*playback
	byTicket map[[32]byte]*playback
	counts   map[string]*counts // by peer, admitted slots included
	ended    map[string]endedPlay
	expired  time.Time // when expiry last ran
}

// counts are what a pairing uses of this server now.
type counts struct {
	streams, transcodes           int
	audioStreams, audioTranscodes int
}

type endedPlay struct {
	peer   string
	reason EndReason
	at     time.Time
}

// playback is a peer's live playback here. Its fields are set once it
// is registered, but used, active and open, which plays.mu guards.
type playback struct {
	id, peer, peerName string
	title, library     string
	handle, method     string
	protocol           string
	audioOnly          bool
	ticket             [32]byte
	started            time.Time
	sheets             int
	subtitles          map[string]bool // track ID: pictures
	slot               *countSlot
	ctx                context.Context
	cancel             context.CancelFunc
	used               time.Time
	active             bool
	open               int
}

// countSlot is a stream counted for a pairing (Admit), and, when
// transcode, one of its transcodes.
type countSlot struct {
	e         *Engine
	peer      string
	audio     bool // counted among audio streams (Limits.Audio)
	limits    Limits
	transcode bool
	released  bool
}

func (c *counts) of(audio bool) (streams, transcodes *int) {
	if audio {
		return &c.audioStreams, &c.audioTranscodes
	}
	return &c.streams, &c.transcodes
}

func (s *countSlot) caps() (streams, transcodes int) {
	if s.audio {
		return s.limits.AudioStreams, s.limits.AudioTranscodes
	}
	return s.limits.Streams, s.limits.Transcodes
}

// Release frees what was counted; once, however often it is called.
func (s *countSlot) Release() {
	ps := &s.e.plays
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if s.released {
		return
	}
	s.released = true
	c := ps.counts[s.peer]
	if c == nil {
		return
	}
	streams, transcodes := c.of(s.audio)
	*streams--
	if s.transcode {
		*transcodes--
	}
	if *c == (counts{}) {
		delete(ps.counts, s.peer)
	}
}

// Transcoding counts a remux that turned out to re-encode the video as a
// transcode from then on (MSFP playback section 6); false when the
// pairing has none left: the host then stops it.
func (s *countSlot) Transcoding() bool {
	ps := &s.e.plays
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if s.transcode {
		return true
	}
	if s.released {
		return false
	}
	_, most := s.caps()
	_, transcodes := ps.countsOf(s.peer).of(s.audio)
	if *transcodes >= most {
		return false
	}
	*transcodes++
	s.transcode = true
	return true
}

func (ps *plays) countsOf(peer string) *counts {
	if ps.counts == nil {
		ps.counts = map[string]*counts{}
	}
	c := ps.counts[peer]
	if c == nil {
		c = &counts{}
		ps.counts[peer] = c
	}
	return c
}

// admitStream counts a stream (and for "transcode" a transcode) for peer, in
// one step no other play comes between (MSFP playback section 6.2).
func (e *Engine) admitStream(peer string, l Limits, audio bool, method string) (*countSlot, error) {
	switch method {
	case "direct", "remux", "transcode":
	default:
		return nil, ErrInternal.with(errors.New("Admit of a method MSFP doesn't have"))
	}
	s := &countSlot{e: e, peer: peer, audio: audio && l.Audio, limits: l}
	most, mostTranscodes := s.caps()
	ps := &e.plays
	ps.mu.Lock()
	defer ps.mu.Unlock()
	c := ps.countsOf(peer)
	streams, transcodes := c.of(s.audio)
	switch {
	case *streams >= most:
		return nil, ErrStreamLimitReached
	case method == "transcode" && *transcodes >= mostTranscodes:
		return nil, ErrServerBusy
	}
	*streams++
	if method == "transcode" {
		*transcodes++
		s.transcode = true
	}
	return s, nil
}

// inUse is how many streams a pairing uses now, of one kind.
func (e *Engine) inUse(peer string, l Limits, audio bool) (used, most int) {
	ps := &e.plays
	ps.mu.Lock()
	defer ps.mu.Unlock()
	s := &countSlot{audio: audio && l.Audio, limits: l}
	most, _ = s.caps()
	if c := ps.counts[peer]; c != nil {
		n, _ := c.of(s.audio)
		used = *n
	}
	return used, most
}

// playRequest is play's request (MSFP playback section 2.1, video section
// 5.1).
type playRequest struct {
	Title            *string         `json:"title"`
	Capabilities     json.RawMessage `json:"capabilities"`
	StartPositionMs  int64           `json:"startPositionMs"`
	MaxBitrateKbps   int64           `json:"maxBitrateKbps"`
	AudioLanguage    string          `json:"audioLanguage"`
	AudioTrack       string          `json:"audioTrack"`
	VersionID        string          `json:"versionId"`
	SubtitleLanguage string          `json:"subtitleLanguage"`
	SubtitleTrack    string          `json:"subtitleTrack"`
}

// requestTag is the form of a language asked for: at most 12 characters
// of A-Z a-z 0-9 - (MSFP playback section 2.1).
var requestTag = regexp.MustCompile(`^[A-Za-z0-9-]{0,12}$`)

// The values capabilities may hold (MSFP playback section 2.1, video
// section 5.1); others are ignored.
var capabilityValues = map[string][]string{
	"containers":  {"mp4", "m4v", "mov", "webm", "mkv", "ts", "mp3", "aac", "m4a", "flac", "ogg", "opus", "wav"},
	"audioCodecs": {"aac", "mp3", "opus", "vorbis", "flac", "ac3", "eac3", "alac", "pcm_s16le"},
	"videoCodecs": {"h264", "hevc", "av1", "vp8", "vp9"},
	"hdr":         {"hdr10", "hlg"},
	"subtitles":   {},
}

// readCapabilities reads play's capabilities as an owning server does:
// false (400) when it isn't an object, or a list isn't a list of strings,
// or a number isn't an integer in range; values MSFP doesn't list
// ignored, each once, sorted.
func readCapabilities(raw json.RawMessage) (Capabilities, bool) {
	if raw == nil {
		return Capabilities{}, true
	}
	var c struct {
		Containers       []any `json:"containers"`
		AudioCodecs      []any `json:"audioCodecs"`
		VideoCodecs      []any `json:"videoCodecs"`
		HDR              []any `json:"hdr"`
		Subtitles        []any `json:"subtitles"`
		MaxAudioChannels int64 `json:"maxAudioChannels"`
		MaxHeight        int64 `json:"maxHeight"`
		MaxBitrateKbps   int64 `json:"maxBitrateKbps"`
	}
	if wire.DecodeEntry(raw, &c) != nil {
		return Capabilities{}, false
	}
	list := func(name string, in []any) ([]string, bool) {
		var out []string
		for _, v := range in {
			s, ok := v.(string)
			if !ok {
				return nil, false
			}
			if slices.Contains(capabilityValues[name], s) && !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
		slices.Sort(out)
		return out, true
	}
	var out Capabilities
	var ok [5]bool
	out.Containers, ok[0] = list("containers", c.Containers)
	out.AudioCodecs, ok[1] = list("audioCodecs", c.AudioCodecs)
	out.VideoCodecs, ok[2] = list("videoCodecs", c.VideoCodecs)
	out.HDR, ok[3] = list("hdr", c.HDR)
	out.Subtitles, ok[4] = list("subtitles", c.Subtitles)
	if slices.Contains(ok[:], false) || c.MaxAudioChannels < 0 || c.MaxAudioChannels > 64 || c.MaxHeight < 0 ||
		c.MaxHeight > 100_000 || c.MaxBitrateKbps < 0 || c.MaxBitrateKbps > maxKbps {
		return Capabilities{}, false
	}
	out.MaxAudioChannels, out.MaxHeight, out.MaxBitrateKbps = int(c.MaxAudioChannels), int(c.MaxHeight), int(c.MaxBitrateKbps)
	return out, true
}

// versionID is a copy's version ID toward a peer: a keyed MAC of its
// handle under the IDs secret, which tells the peer nothing of it (MSFP
// catalog section 2.1), stable as long as the handle is.
func (e *Engine) versionID(peer, handle string) string {
	m := hmac.New(sha256.New, e.ourKeys().ids)
	m.Write([]byte("msfp version\n" + peer + "\n" + handle))
	return "v" + wire.Encode64(m.Sum(nil)[:16])
}

// sameLanguage says whether a tag matches the one asked: exactly
// (compared without case) when exact, else by base language.
func sameLanguage(tag, asked string, exact bool) bool {
	if tag == "" || asked == "" {
		return false
	}
	if exact {
		return strings.EqualFold(tag, asked)
	}
	base := func(s string) string { b, _, _ := strings.Cut(s, "-"); return strings.ToLower(b) }
	return base(tag) == base(asked)
}

// chooseCopy picks the copy versionId names, else the first whose audio
// has the language (same tag, then same base language), else the first
// (MSFP playback section 2.6).
func chooseCopy(copies []Copy, version func(Copy) string, versionID, language string) int {
	if versionID != "" {
		for i, c := range copies {
			if version(c) == versionID {
				return i
			}
		}
	}
	for _, exact := range []bool{true, false} {
		for i, c := range copies {
			if slices.ContainsFunc(c.Audio, func(a string) bool { return sameLanguage(a, language, exact) }) {
				return i
			}
		}
	}
	return 0
}

// chooseAudio picks the audio track asked, if the copy has it; else the
// first of the title's own sound (no commentary, no description) in the
// language, same tag then same base language; else the default audio:
// the track marked default, else the first (MSFP playback section 2.6).
func chooseAudio(tracks []Track, id, language string) string {
	if slices.ContainsFunc(tracks, func(t Track) bool { return t.ID == id }) {
		return id
	}
	for _, exact := range []bool{true, false} {
		for _, t := range tracks {
			if t.Role == "" && sameLanguage(t.Language, language, exact) {
				return t.ID
			}
		}
	}
	for _, t := range tracks {
		if t.Default {
			return t.ID
		}
	}
	if len(tracks) > 0 {
		return tracks[0].ID
	}
	return ""
}

// chooseSubtitles picks the subtitles shown (MSFP video section 5.4):
// none for "off"; the track asked, if the copy has it; else the first in
// the language (same tag then same base language, forced ones last);
// else none.
func chooseSubtitles(tracks []Track, id, language string) string {
	if id == "off" {
		return ""
	}
	if slices.ContainsFunc(tracks, func(t Track) bool { return t.ID == id }) {
		return id
	}
	for _, forced := range []bool{false, true} {
		for _, exact := range []bool{true, false} {
			for _, t := range tracks {
				if t.Forced == forced && sameLanguage(t.Language, language, exact) {
					return t.ID
				}
			}
		}
	}
	return ""
}

// forcedFor is the forced subtitle track in the language of the audio
// played, when the copy has one (MSFP video section 5.4).
func forcedFor(tracks []Track, audio string) string {
	for _, exact := range []bool{true, false} {
		for _, t := range tracks {
			if t.Forced && sameLanguage(t.Language, audio, exact) {
				return t.ID
			}
		}
	}
	return ""
}

// cleanTracks keeps of the host's tracks those MSFP can send: an ID of
// the kind's form ("a<n>", "s<n>"), once; a label, cleaned. A role other
// than commentary or description is none.
func cleanTracks(tracks []Track, kind byte) []Track {
	var out []Track
	for _, t := range tracks {
		if _, ok := wire.TrackID(t.ID, kind); !ok || slices.ContainsFunc(out, func(o Track) bool { return o.ID == t.ID }) {
			continue
		}
		if t.Label = wire.CleanText(t.Label, 64); t.Label == "" {
			continue
		}
		if !wire.LanguageTag(t.Language) {
			t.Language = ""
		}
		if t.Role != "commentary" && t.Role != "description" {
			t.Role = ""
		}
		if t.Channels < 0 || t.Channels > 64 {
			t.Channels = 0
		}
		if kind == 'a' {
			t.Pictures = false
		}
		out = append(out, t)
	}
	return out
}

// segmentKinds are the skip markers MSFP has (video section 5.3).
var segmentKinds = []string{"intro", "outro", "recap", "preview", "commercial"}

// cleanSegments keeps skip markers of a known kind within 100 days, 32
// at most.
func cleanSegments(in []Segment) []Segment {
	var out []Segment
	for _, s := range in {
		if len(out) < 32 && slices.Contains(segmentKinds, s.Kind) && s.StartMs >= 0 && s.EndMs > s.StartMs && s.EndMs <= maxPositionMs {
			out = append(out, s)
		}
	}
	return out
}

// playAnswer is play's answer (MSFP playback section 2.3, video section
// 5.3).
type playAnswer struct {
	ID                     string         `json:"id"`
	Method                 string         `json:"method"`
	Stream                 streamWire     `json:"stream"`
	PositionMs             int64          `json:"positionMs"`
	DurationMs             int64          `json:"durationMs,omitempty"`
	Versions               []Version      `json:"versions,omitempty"`
	VersionID              string         `json:"versionId,omitempty"`
	AudioTracks            []Track        `json:"audioTracks,omitempty"`
	AudioTrack             string         `json:"audioTrack,omitempty"`
	MaxBitrateKbps         int            `json:"maxBitrateKbps,omitempty"`
	Info                   *PlaybackInfo  `json:"info,omitempty"`
	SubtitleTracks         []subtitleWire `json:"subtitleTracks,omitempty"`
	SubtitleTrack          string         `json:"subtitleTrack,omitempty"`
	ForcedTracks           []subtitleWire `json:"forcedTracks,omitempty"`
	ForcedTrack            string         `json:"forcedTrack,omitempty"`
	SubtitleStretchSeconds int            `json:"subtitleStretchSeconds,omitempty"`
	Segments               []Segment      `json:"segments,omitempty"`
	Trickplay              *trickplayWire `json:"trickplay,omitempty"`
}

type streamWire struct {
	URL      string `json:"url"`
	Protocol string `json:"protocol"`
	MimeType string `json:"mimeType,omitempty"`
}

type subtitleWire struct {
	Track
	URL string `json:"url,omitempty"`
}

type trickplayWire struct {
	TileWidth  int      `json:"tileWidth"`
	TileHeight int      `json:"tileHeight"`
	Columns    int      `json:"columns"`
	Rows       int      `json:"rows"`
	IntervalMs int      `json:"intervalMs"`
	TileCount  int      `json:"tileCount"`
	Sheets     []string `json:"sheets"`
}

// play starts a paired peer's playback of a title here (MSFP playback
// section 2.2), in its steps.
func (e *Engine) play(req *request) response {
	p := req.peer
	if p == nil || p.State != Paired {
		return refuse(probNotFound)
	}
	if !e.on(p, wire.PartPlayback) {
		return refuse(probNotSupported)
	}
	var in playRequest
	if wire.DecodeJSON(req.body, &in) != nil || in.Title == nil {
		return refuse(probInvalid)
	}
	// 1. A key play takes, of a content part on; numbers and tags in range.
	k, ok := e.keyOn(p, *in.Title)
	caps, capsOK := readCapabilities(in.Capabilities)
	if !ok || !k.Playable || !capsOK || in.StartPositionMs < 0 || in.StartPositionMs > maxPositionMs ||
		in.MaxBitrateKbps < 0 || in.MaxBitrateKbps > maxKbps || !requestTag.MatchString(in.AudioLanguage) {
		return refuse(probInvalid)
	}
	video := k.Part == wire.PartVideo
	if video && !requestTag.MatchString(in.SubtitleLanguage) {
		return refuse(probInvalid)
	}
	audioOnly := k.Part == wire.PartMusic
	// 2. No stream at all for this pairing.
	if _, most := e.inUse(p.ID, p.Limits, audioOnly); most == 0 {
		return refuse(probStreamLimit)
	}
	// 3. Copies of that key in the libraries offered to it.
	if e.cfg.Catalog == nil || e.cfg.Playback == nil || len(p.Offers) == 0 {
		return refuse(probNotFound)
	}
	ctx := req.r.Context()
	copies, err := e.cfg.Catalog.Copies(ctx, *in.Title, p.Offers)
	if err != nil {
		e.log.Warn("msfp: the host's copies couldn't be read", "error", err)
		return refuse(probInternal)
	}
	copies = slices.DeleteFunc(copies, func(c Copy) bool { return c.Library != "" && !slices.Contains(p.Offers, c.Library) })
	if len(copies) == 0 {
		return refuse(probNotFound)
	}
	// 4. The copy, then malformed track IDs refused.
	version := func(c Copy) string { return e.versionID(p.ID, c.Handle) }
	chosen := copies[chooseCopy(copies, version, in.VersionID, in.AudioLanguage)]
	if _, ok := wire.TrackID(in.AudioTrack, 'a'); in.AudioTrack != "" && !ok {
		return refuse(probInvalid)
	}
	if _, ok := wire.TrackID(in.SubtitleTrack, 's'); video && in.SubtitleTrack != "" && in.SubtitleTrack != "off" && !ok {
		return refuse(probInvalid)
	}
	// 5. The pairing's streams all in use: refused before any work (the
	// count that matters is Admit's).
	if used, most := e.inUse(p.ID, p.Limits, audioOnly); used >= most {
		return refuse(probStreamLimit)
	}
	info, err := e.cfg.Playback.Describe(ctx, chosen.Handle)
	if err != nil {
		return e.hostRefusal(err, "describe")
	}
	audioTracks := cleanTracks(info.AudioTracks, 'a')
	var subtitles []Track
	if video {
		subtitles = cleanTracks(info.SubtitleTracks, 's')
	}
	audio := chooseAudio(audioTracks, in.AudioTrack, in.AudioLanguage)
	shown := ""
	forced := ""
	if video {
		shown = chooseSubtitles(subtitles, in.SubtitleTrack, in.SubtitleLanguage)
		audioLang := ""
		if i := slices.IndexFunc(audioTracks, func(t Track) bool { return t.ID == audio }); i >= 0 {
			audioLang = audioTracks[i].Language
		}
		forced = forcedFor(subtitles, audioLang)
	}
	// 6. The bitrate cap: the lowest of the four, 120,000 without one.
	kbps := 0
	for _, c := range []int{int(in.MaxBitrateKbps), caps.MaxBitrateKbps, e.cfg.MaxBitrateKbps, p.Limits.BitrateKbps} {
		if c > 0 && (kbps == 0 || c < kbps) {
			kbps = c
		}
	}
	if kbps == 0 {
		kbps = defaultKbps
	}
	start := in.StartPositionMs
	if start == 0 && video && info.EpisodeStartMs > 0 {
		start = info.EpisodeStartMs
	}
	// 7. The host starts it, admitted by the pairing's limits first.
	var slot *countSlot
	sr := StartRequest{Copy: chosen.Handle, Title: *in.Title, AudioOnly: audioOnly, Peer: PeerRef{ID: p.ID, Name: p.Name},
		AudioTrack: audio, SubtitleTrack: shown, StartMs: start, Capabilities: caps, MaxBitrateKbps: kbps,
		Admit: func(method string) (Slot, error) {
			if slot != nil {
				return nil, ErrInternal.with(errors.New("Admit called twice"))
			}
			s, err := e.admitStream(p.ID, p.Limits, audioOnly, method)
			if err != nil {
				return nil, err
			}
			slot = s
			return s, nil
		}}
	started, err := e.cfg.Playback.Start(ctx, sr)
	if err != nil {
		if slot != nil {
			slot.Release()
		}
		return e.hostRefusal(err, "start")
	}
	if slot == nil {
		// A host that didn't ask: admitted now, its playback stopped when
		// the limits refuse it.
		if slot, err = e.admitStream(p.ID, p.Limits, audioOnly, started.Method); err != nil {
			e.hostStop(started.Handle, EndStopped)
			return e.hostRefusal(err, "admit")
		}
	}
	pb, res := e.register(req, p, *in.Title, chosen, info, started, slot, audioOnly)
	if pb == nil {
		return res.response
	}
	// The pairing ended, or the library is no longer offered, while it
	// started: it ends at once.
	if q, err := e.peerRecord(ctx, p.ID); err != nil || q.State != Paired || !q.Suspended.IsZero() ||
		chosen.Library != "" && !slices.Contains(q.Offers, chosen.Library) {
		e.endPlayback(pb, EndNotOffered)
		return refuse(probNotFound)
	}
	// 8. The answer.
	out := playAnswer{ID: pb.id, Method: started.Method, PositionMs: started.PositionMs, MaxBitrateKbps: kbps,
		Info: cleanInfo(started.Info), VersionID: version(chosen), AudioTracks: audioTracks, AudioTrack: audio}
	if info.DurationMs > 0 && info.DurationMs <= maxPositionMs {
		out.DurationMs = info.DurationMs
	}
	base := e.streamBase(req) + res.ticketText + "/"
	out.Stream = streamWire{URL: base + "file", Protocol: "file", MimeType: wire.RelayedMediaType(started.MimeType)}
	if started.Protocol == "hls" {
		out.Stream = streamWire{URL: base + "master.m3u8", Protocol: "hls", MimeType: "application/vnd.apple.mpegurl"}
	}
	for _, c := range copies {
		if n := wire.CleanText(c.Name, 64); n != "" {
			out.Versions = append(out.Versions, Version{ID: version(c), Name: n})
		}
	}
	if video {
		for _, t := range subtitles {
			w := subtitleWire{Track: t, URL: wire.SubtitleURL(base, t.ID, t.Pictures)}
			out.SubtitleTracks = append(out.SubtitleTracks, w)
			if t.Forced {
				out.ForcedTracks = append(out.ForcedTracks, w)
			}
		}
		out.SubtitleTrack, out.ForcedTrack = shown, forced
		if len(subtitles) > 0 {
			out.SubtitleStretchSeconds = wire.StretchSeconds
		}
		out.Segments = cleanSegments(started.Segments)
		if pb.sheets > 0 {
			tp := info.Trickplay
			out.Trickplay = &trickplayWire{TileWidth: tp.TileWidth, TileHeight: tp.TileHeight, Columns: tp.Columns,
				Rows: tp.Rows, IntervalMs: tp.IntervalMs, TileCount: tp.TileCount, Sheets: []string{}}
			for n := range pb.sheets {
				out.Trickplay.Sheets = append(out.Trickplay.Sheets, wire.SheetURL(base, n))
			}
		}
	}
	e.log.Info("msfp: a peer's playback started", "peer", p.ID, "playback", pb.id, "method", started.Method)
	e.notify(PlaybackStarted{Peer: p.ID, Name: p.Name, Playback: pb.id, Title: pb.title, Method: pb.method})
	return answer(out)
}

// registered is what register gives besides the playback: the ticket's
// text, for the URLs only.
type registered struct {
	response
	ticketText string
}

// register makes a started playback live: its ID and its ticket, 128
// bits and 256 bits of randomness, the ticket kept by its hash only
// (MSFP media section 1).
func (e *Engine) register(req *request, p *PeerRecord, title string, c Copy, info CopyInfo, s Started, slot *countSlot, audioOnly bool) (*playback, registered) {
	fail := func() (*playback, registered) {
		slot.Release()
		e.hostStop(s.Handle, EndStopped)
		return nil, registered{response: refuse(probInternal)}
	}
	if s.Method != "direct" && s.Method != "remux" && s.Method != "transcode" || s.Protocol != "hls" && s.Protocol != "file" {
		e.log.Warn("msfp: the host started a playback with a method or protocol MSFP doesn't have")
		return fail()
	}
	idBytes, err := e.random(16)
	if err != nil {
		return fail()
	}
	ticketBytes, err := e.random(32)
	if err != nil {
		return fail()
	}
	ticket := wire.Encode64(ticketBytes)
	now := e.now()
	pb := &playback{id: "play_" + lowerBase32.EncodeToString(idBytes), peer: p.ID, peerName: p.Name, title: title,
		library: c.Library, handle: s.Handle, method: s.Method, protocol: s.Protocol, audioOnly: audioOnly,
		ticket: sha256.Sum256([]byte(ticket)), started: now, used: now, slot: slot, subtitles: map[string]bool{}}
	if tp := info.Trickplay; tp != nil && !audioOnly {
		t := wire.Trickplay{TileWidth: tp.TileWidth, TileHeight: tp.TileHeight, Columns: tp.Columns, Rows: tp.Rows,
			IntervalMs: tp.IntervalMs, TileCount: tp.TileCount}
		if t.Sheets = t.SheetCount(); t.InBounds() {
			pb.sheets = t.Sheets
		}
	}
	if !audioOnly {
		for _, t := range cleanTracks(info.SubtitleTracks, 's') {
			pb.subtitles[t.ID] = t.Pictures
		}
	}
	pb.ctx, pb.cancel = context.WithCancel(context.Background())
	ps := &e.plays
	ps.mu.Lock()
	if ps.byID == nil {
		ps.byID, ps.byTicket = map[string]*playback{}, map[[32]byte]*playback{}
	}
	ps.byID[pb.id], ps.byTicket[pb.ticket] = pb, pb
	ps.mu.Unlock()
	return pb, registered{ticketText: ticket}
}

// streamBase is the stream root of the address the request was signed
// for: the former one during a move, for a peer still on it (MSFP media
// section 1, core section 13.1, step 5).
func (e *Engine) streamBase(req *request) string {
	a := e.address()
	addr := a.address
	if req.v.Former && a.former != "" {
		addr = a.former
	}
	return addr + e.cfg.Prefix + "/api/v1/stream/"
}

// cleanInfo is the host's information made plain text, short.
func cleanInfo(in *PlaybackInfo) *PlaybackInfo {
	if in == nil {
		return nil
	}
	out := &PlaybackInfo{Container: wire.CleanText(in.Container, 32), BitrateKbps: max(in.BitrateKbps, 0)}
	for _, r := range in.Reasons {
		if r = wire.CleanText(r, 64); r != "" && len(out.Reasons) < 16 {
			out.Reasons = append(out.Reasons, r)
		}
	}
	for _, pair := range [][2]**StreamInfo{{&in.Video, &out.Video}, {&in.Audio, &out.Audio}} {
		if s := *pair[0]; s != nil {
			c := *s
			c.Codec, c.Profile, c.Range = wire.CleanText(c.Codec, 32), wire.CleanText(c.Profile, 32), wire.CleanText(c.Range, 32)
			if !wire.LanguageTag(c.Language) {
				c.Language = ""
			}
			*pair[1] = &c
		}
	}
	return out
}

// hostRefusal is the host's error as the peer is told it (engine.md
// section 6).
func (e *Engine) hostRefusal(err error, what string) response {
	switch {
	case errors.Is(err, ErrNotPlayable):
		return refuse(probNotPlayable)
	case errors.Is(err, ErrStreamLimitReached):
		return refuse(probStreamLimit)
	case errors.Is(err, ErrServerBusy):
		return refuse(probServerBusy)
	case errors.Is(err, ErrNotFound):
		return refuse(probNotFound)
	}
	e.log.Warn("msfp: the host's playback failed", "step", what, "error", err)
	return refuse(probInternal)
}

// hostStop tells the host a playback ended, in the background.
func (e *Engine) hostStop(handle string, why EndReason) {
	if e.cfg.Playback == nil {
		return
	}
	e.background(func(ctx context.Context) {
		defer func() {
			if v := recover(); v != nil {
				e.log.Error("msfp: the host's Stop crashed", "panic", v)
			}
		}()
		ctx, cancel := context.WithTimeout(ctx, hostTime)
		defer cancel()
		if err := e.cfg.Playback.Stop(ctx, handle, why); err != nil {
			e.log.Warn("msfp: the host's Stop failed", "error", err)
		}
	})
}

// livePlayback is a peer's live playback by its ID; nil when it isn't
// live, or isn't that peer's.
func (e *Engine) livePlayback(peer, id string) *playback {
	ps := &e.plays
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if pb := ps.byID[id]; pb != nil && pb.peer == peer {
		return pb
	}
	return nil
}

// endPlayback ends a live playback, once: its ticket stops working at
// once, its open responses are cut, its stream is freed, the host is
// told (MSFP playback section 6.3).
func (e *Engine) endPlayback(pb *playback, why EndReason) {
	ps := &e.plays
	ps.mu.Lock()
	if ps.byID[pb.id] != pb {
		ps.mu.Unlock()
		return
	}
	delete(ps.byID, pb.id)
	delete(ps.byTicket, pb.ticket)
	if ps.ended == nil {
		ps.ended = map[string]endedPlay{}
	}
	ps.ended[pb.id] = endedPlay{peer: pb.peer, reason: why, at: e.now()}
	ps.mu.Unlock()
	pb.cancel()
	pb.slot.Release()
	e.hostStop(pb.handle, why)
	e.log.Info("msfp: a peer's playback ended", "peer", pb.peer, "playback", pb.id, "why", why)
	e.notify(PlaybackEnded{Peer: pb.peer, Name: pb.peerName, Playback: pb.id, Title: pb.title, Reason: why})
}

// endPlaybacks ends every live playback for which which says so.
func (e *Engine) endPlaybacks(which func(*playback) bool, why EndReason) {
	ps := &e.plays
	ps.mu.Lock()
	var end []*playback
	for _, pb := range ps.byID {
		if which(pb) {
			end = append(end, pb)
		}
	}
	ps.mu.Unlock()
	for _, pb := range end {
		e.endPlayback(pb, why)
	}
}

// expirePlaybacks ends the playbacks with neither progress nor a media
// request for 2 minutes (an open response counts as use), and those
// longer than LongestPlayback; it forgets endings over an hour old.
func (e *Engine) expirePlaybacks(force bool) {
	now := e.now()
	ps := &e.plays
	ps.mu.Lock()
	if !force && now.Sub(ps.expired) < expiryEvery {
		ps.mu.Unlock()
		return
	}
	ps.expired = now
	var expired, tooLong []*playback
	for _, pb := range ps.byID {
		if pb.open > 0 {
			pb.used = now
		}
		switch {
		case now.Sub(pb.started) >= e.cfg.LongestPlayback:
			tooLong = append(tooLong, pb)
		case now.Sub(pb.used) >= expireAfter:
			expired = append(expired, pb)
		}
	}
	for id, ended := range ps.ended {
		if now.Sub(ended.at) >= endedKept {
			delete(ps.ended, id)
		}
	}
	ps.mu.Unlock()
	for _, pb := range tooLong {
		e.endPlayback(pb, EndTooLong)
	}
	for _, pb := range expired {
		e.endPlayback(pb, EndExpired)
	}
}

// used notes a playback's use: a progress, a media request.
func (e *Engine) used(pb *playback) {
	ps := &e.plays
	ps.mu.Lock()
	pb.used, pb.active = e.now(), true
	ps.mu.Unlock()
}

type progressRequest struct {
	ID         *string `json:"id"`
	PositionMs int64   `json:"positionMs"`
	Paused     bool    `json:"paused"`
}

// progress is a peer telling that a playback is still watched (MSFP
// playback section 3).
func (e *Engine) progress(req *request) response {
	p := req.peer
	if p == nil || p.State != Paired {
		return refuse(probNotFound)
	}
	if !e.on(p, wire.PartPlayback) {
		return refuse(probNotSupported)
	}
	var in progressRequest
	if wire.DecodeJSON(req.body, &in) != nil || in.ID == nil || !wire.PlayID(*in.ID) || in.PositionMs < 0 || in.PositionMs > maxPositionMs {
		return refuse(probInvalid)
	}
	pb := e.livePlayback(p.ID, *in.ID)
	if pb == nil {
		return refuse(probStopped)
	}
	e.used(pb)
	if err := e.cfg.Playback.Progress(req.r.Context(), pb.handle, in.PositionMs, in.Paused); err != nil {
		e.log.Info("msfp: the host's Progress failed", "error", err)
	}
	return answer(struct{}{})
}

type stopRequest struct {
	ID         *string `json:"id"`
	PositionMs int64   `json:"positionMs"`
}

// stop is a peer telling that a playback is over (MSFP playback section
// 4): 200 whether or not it was live; another pairing's changes nothing.
func (e *Engine) stop(req *request) response {
	p := req.peer
	if p == nil || p.State != Paired {
		return refuse(probNotFound)
	}
	if !e.on(p, wire.PartPlayback) {
		return refuse(probNotSupported)
	}
	var in stopRequest
	if wire.DecodeJSON(req.body, &in) != nil || in.ID == nil || !wire.PlayID(*in.ID) || in.PositionMs < 0 || in.PositionMs > maxPositionMs {
		return refuse(probInvalid)
	}
	if pb := e.livePlayback(p.ID, *in.ID); pb != nil {
		if in.PositionMs > 0 {
			if err := e.cfg.Playback.Progress(req.r.Context(), pb.handle, in.PositionMs, false); err != nil {
				e.log.Info("msfp: the host's Progress failed", "error", err)
			}
		}
		e.endPlayback(pb, EndStopped)
	}
	return answer(struct{}{})
}

// StopPlayback is the operator stopping a peer's playback here. Error:
// ErrNotFound.
func (e *Engine) StopPlayback(id string) error {
	ps := &e.plays
	ps.mu.Lock()
	pb := ps.byID[id]
	ps.mu.Unlock()
	if pb == nil {
		return ErrNotFound
	}
	e.endPlayback(pb, EndOperator)
	return nil
}

// PlaybackView is a peer's playback here, for the host's live view: the
// peer (never a person), the title played, how, since when, and whether
// it was used since it started ("started" or "active").
type PlaybackView struct {
	ID, Peer, PeerName, Title, Method string
	Since                             time.Time
	State                             string
}

// Playbacks lists the peers' playbacks here, live, oldest first.
func (e *Engine) Playbacks() []PlaybackView {
	ps := &e.plays
	ps.mu.Lock()
	defer ps.mu.Unlock()
	out := make([]PlaybackView, 0, len(ps.byID))
	for _, pb := range ps.byID {
		v := PlaybackView{ID: pb.id, Peer: pb.peer, PeerName: pb.peerName, Title: pb.title, Method: pb.method,
			Since: pb.started, State: "started"}
		if pb.active {
			v.State = "active"
		}
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b PlaybackView) int { return cmp.Or(a.Since.Compare(b.Since), strings.Compare(a.ID, b.ID)) })
	return out
}

// playAllowed says whether a peer may still use a playback here: paired,
// not suspended (MSFP media section 4). Read from storage at each media
// request, so an ending noticed only then still stops the ticket.
func (e *Engine) playAllowed(ctx context.Context, pb *playback) bool {
	p, err := e.peerRecord(ctx, pb.peer)
	switch {
	case errors.Is(err, ErrNotFound):
		e.endPlayback(pb, EndPairingEnded)
		return false
	case err != nil:
		return false
	case p.State != Paired || !p.Suspended.IsZero():
		e.endPlayback(pb, EndPairingEnded)
		return false
	case pb.library != "" && !slices.Contains(p.Offers, pb.library):
		e.endPlayback(pb, EndNotOffered)
		return false
	}
	return true
}
