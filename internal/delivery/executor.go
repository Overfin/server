package delivery

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/msfp-io/msfp-go/wire"
	"github.com/overfin/server/internal/files"
	"github.com/overfin/server/internal/store"
)

// Executor carries out plays of the copy index's copies.
type Executor struct {
	// Maker makes remuxes and transcodes; nil: only files are served.
	Maker Maker
	// Path is where Jellyfin keeps a version's file. It's only ever opened
	// through Libraries' checks (rule 78), and never logged or kept but in
	// the playback's memory.
	Path func(ctx context.Context, itemID, sourceID string) (string, error)
	// Libraries are the libraries' folders, the only ones read.
	Libraries func(ctx context.Context) (files.Libraries, error)
	// Fingerprint is a version's file's fingerprint (files.Cache's).
	Fingerprint func(ctx context.Context, itemID, sourceID string, f *files.File) (string, error)
	// OriginalFile is the operator's switch "players may get the original
	// file" (on by default), for Overfin's own players; nil: on.
	OriginalFile func() bool
}

// Request is a play as the executor reads it: MSFP's play request (its
// track IDs checked by the caller), the cap in effect (the profile's or
// the pairing's, the server's, the request's: the lowest; 0 for none),
// and whose it is: Own for Overfin's own players (never a burned
// subtitle: rule 17), else a paired server's, MayBurn its operator's
// burn-in switch (off by default).
type Request struct {
	Play    wire.PlayRequest
	CapKbps int
	Own     bool
	MayBurn bool
}

// Plan is what a play makes of a copy, decided before anything starts:
// the audio track played, what is served of each stream (wire.Served),
// the kind made, and what is converted. VideoConverted takes a transcode
// slot; a playback whose video is copied (a remux, or only its audio
// converted) counts as a stream only (D8, revised).
type Plan struct {
	Copy           store.IndexedCopy
	Audio          string
	Served         wire.Served
	Kind           string
	VideoConverted bool
	AudioConverted bool
	StartMs        int64
	// order is what the Maker is asked; nil for a file.
	order *Order
}

// Method is the play answer's method: direct, remux or transcode.
func (p *Plan) Method() string {
	switch {
	case p.Kind == KindFile:
		return wire.MethodDirect
	case p.VideoConverted || p.AudioConverted:
		return wire.MethodTranscode
	}
	return wire.MethodRemux
}

// Order is what the Maker will be asked (a copy of it); nil for a file.
func (p *Plan) Order() *Order {
	if p.order == nil {
		return nil
	}
	o := *p.order
	return &o
}

// hasPicture says whether a copy is a video: its picture's facts, or its
// media type when they aren't known (a minimal copy).
func hasPicture(c wire.Copy) bool {
	return c.Video != nil || strings.HasPrefix(c.MediaType, "video/")
}

// Plan decides what a play makes of c, or refuses it: MSFP's refusals
// (wire.Serve: 409 delivery-refused), and Overfin's own: a file Overfin
// hasn't read (a minimal copy) never served or copied as it is, the
// original file only when the operator lets players have it, no burned
// subtitles (never for own players; for peers only with the operator's
// switch, and no Maker burns yet), HDR to SDR only with tone mapping, an
// audio track Jellyfin's numbering doesn't map.
func (e *Executor) Plan(ctx context.Context, c store.IndexedCopy, r Request) (*Plan, error) {
	req := r.Play
	if req.Transcode != nil && req.Transcode.BurnSubtitle != "" {
		switch {
		case r.Own:
			return nil, refused("burned-subtitles")
		case !r.MayBurn:
			return nil, refused("burn-in-off")
		}
		return nil, refused("burn-in-not-made")
	}
	facts := c.Facts
	facts.ID, facts.ETag = c.CopyID, c.ETag
	audio := wire.DefaultAudio(facts.AudioTracks, req.AudioTrack)
	var enc Encoders
	if req.Delivery != wire.DeliveryFile {
		if e.Maker == nil {
			return nil, refused("no-maker")
		}
		var err error
		if enc, err = e.Maker.Encoders(ctx); err != nil {
			return nil, busy("encoders-unknown")
		}
	}
	served, err := wire.Serve(req, facts, audio, r.CapKbps, enc.Wire())
	if err != nil {
		if rf := AsRefusal(err); rf != nil {
			return nil, rf
		}
		return nil, err
	}
	p := &Plan{Copy: c, Audio: audio, Served: served, StartMs: min(max(req.StartPositionMs, 0), max(facts.DurationMs, 0))}
	picture := hasPicture(facts)
	switch {
	case req.Delivery == wire.DeliveryFile:
		switch {
		case c.Minimal:
			return nil, refused("not-read")
		case r.Own && e.OriginalFile != nil && !e.OriginalFile():
			return nil, refused("original-file-off")
		case e.Path == nil || e.Libraries == nil || e.Fingerprint == nil:
			return nil, refused("no-files")
		}
		p.Kind = KindFile
		return p, nil
	case c.Minimal && (served.Video == wire.CodecCopy || served.Audio == wire.CodecCopy):
		return nil, refused("not-read") // its facts are Jellyfin's: never copied as they are
	}
	// A picture whose facts aren't known (a minimal copy) is converted:
	// to the codec asked, else the baseline's.
	videoOut := served.Video
	if facts.Video == nil && picture {
		if t := req.Transcode; t != nil && t.VideoCodec == wire.CodecCopy {
			return nil, refused("not-read")
		}
		videoOut = wire.BaselineVideo
		if t := req.Transcode; req.Delivery == wire.DeliveryTranscode && t != nil && t.VideoCodec != "" && t.VideoCodec != wire.CodecCopy {
			videoOut = t.VideoCodec
		}
		if !slices.Contains(enc.Wire().Video, videoOut) && videoOut != wire.BaselineVideo {
			return nil, refused("video-codec-not-made")
		}
	}
	p.VideoConverted = picture && videoOut != wire.CodecCopy
	p.AudioConverted = audio != "" && served.Audio != wire.CodecCopy
	p.Kind = KindRemux
	if p.VideoConverted || p.AudioConverted {
		p.Kind = KindTranscode
	}

	o := &Order{ItemID: c.ItemID, SourceID: c.SourceID, StartMs: p.StartMs}
	if picture {
		v := &VideoOrder{Copy: !p.VideoConverted}
		if p.VideoConverted {
			if err := e.convertVideo(v, videoOut, facts, served, req, r.CapKbps, enc); err != nil {
				return nil, err
			}
		}
		o.Video = v
	}
	if audio != "" {
		index, ok := c.Streams[audio]
		if !ok {
			return nil, refused("track-not-mapped")
		}
		a := &AudioOrder{Index: index, Copy: !p.AudioConverted}
		if p.AudioConverted {
			a.Codec, a.Channels = served.Audio, wire.BaselineChannels
			if served.Transcode != nil && served.Transcode.AudioChannels > 0 {
				a.Channels = served.Transcode.AudioChannels
			}
			if t := track(facts, audio); t != nil && t.Channels > 0 {
				a.Channels = min(a.Channels, t.Channels)
			}
		}
		o.Audio = a
	}
	segments, err := segmentsFor(p, o, facts, audio, req)
	if err != nil {
		return nil, err
	}
	o.Segments = segments
	p.order = o
	return p, nil
}

// convertVideo fills a converted picture's order: the codec, a height
// (the one asked, else the copy's; never above the copy's), a bitrate
// (the one asked, else one for the height; never above the cap, the
// audio's share left), tone mapped when an HDR picture is made SDR.
func (e *Executor) convertVideo(v *VideoOrder, codec string, facts wire.Copy, served wire.Served, req wire.PlayRequest, capKbps int, enc Encoders) error {
	v.Codec = codec
	var t wire.TranscodeTargets
	if served.Transcode != nil {
		t = *served.Transcode
	}
	height := t.MaxHeight
	if facts.Video != nil && facts.Video.Height > 0 && (height == 0 || height > facts.Video.Height) {
		height = facts.Video.Height
	}
	if height == 0 {
		height = 1080
	}
	v.MaxHeight = height
	v.HeightAsked = t.MaxHeight > 0
	kbps := t.MaxBitrateKbps
	if kbps == 0 {
		kbps = defaultKbps(height)
	}
	if limit := lowestPositive(capKbps, req.MaxBitrateKbps); limit > 0 {
		kbps = min(kbps, max(limit-audioKbps(served), 100))
	}
	v.MaxBitrate = kbps * 1000
	hdr := facts.Video != nil && facts.Video.HighDynamicRange()
	keep := t.Range == wire.RangeKeep
	switch {
	case hdr && !keep && !enc.ToneMapping:
		return refused("tone-mapping-off")
	case hdr && keep && !enc.KeepsHDR:
		return refused("hdr-kept-needs-gpu")
	}
	v.ToneMap = hdr && !keep
	return nil
}

// defaultKbps is a converted picture's bitrate when none is asked: what
// its height needs, within Jellyfin's table of sizes by bitrate.
func defaultKbps(height int) int {
	switch {
	case height <= 480:
		return 4_000
	case height <= 720:
		return 8_000
	case height <= 1080:
		return 16_000
	case height <= 1440:
		return 30_000
	}
	return 50_000
}

// audioKbps is the share of a cap the audio takes: converted, what the
// Maker gives it; copied, a generous guess.
func audioKbps(s wire.Served) int {
	if s.Audio == wire.CodecCopy || s.Audio == "" {
		return 640
	}
	if s.Transcode != nil && s.Transcode.AudioChannels > 2 {
		return 384
	}
	return 192
}

func lowestPositive(values ...int) int {
	m := 0
	for _, v := range values {
		if v > 0 && (m == 0 || v < m) {
			m = v
		}
	}
	return m
}

func track(c wire.Copy, id string) *wire.CopyAudio {
	for i := range c.AudioTracks {
		if c.AudioTracks[i].ID == id {
			return &c.AudioTracks[i]
		}
	}
	return nil
}

// segmentsFor is the segment format of a remux or a transcode: a remux's
// as Serve read it from its media type; a transcode's from its media type
// when it names one (video/mp2t, video/mp4), else MPEG-TS for H.264 with
// what TS carries, fragmented MP4 for the rest.
func segmentsFor(p *Plan, o *Order, c wire.Copy, audio string, req wire.PlayRequest) (string, error) {
	var codecs []string
	if o.Video != nil {
		if o.Video.Copy {
			if c.Video != nil {
				codecs = append(codecs, c.Video.Codec)
			}
		} else {
			codecs = append(codecs, o.Video.Codec)
		}
	}
	if o.Audio != nil {
		if o.Audio.Copy {
			if t := track(c, audio); t != nil {
				codecs = append(codecs, t.Codec)
			}
		} else {
			codecs = append(codecs, o.Audio.Codec)
		}
	}
	ts := wire.Encoders{}.Carries(wire.SegmentsTS, codecs...)
	mp4 := wire.Encoders{}.Carries(wire.SegmentsMP4, codecs...)
	sub := p.Served.Segments
	if sub == "" && req.MediaType != "" {
		_, sub, _ = strings.Cut(req.MediaType, "/")
		if sub != wire.SegmentsTS && sub != wire.SegmentsMP4 {
			return "", refused("segments")
		}
	}
	switch sub {
	case wire.SegmentsTS:
		if !ts {
			return "", refused("segments-cannot-carry")
		}
		return SegmentsTS, nil
	case wire.SegmentsMP4:
		if !mp4 {
			return "", refused("segments-cannot-carry")
		}
		return SegmentsMP4, nil
	}
	if ts && (o.Video == nil || len(codecs) > 0 && codecs[0] == wire.BaselineVideo) {
		return SegmentsTS, nil
	}
	if mp4 {
		return SegmentsMP4, nil
	}
	return "", refused("segments-cannot-carry")
}

// Started is a delivery made: its plan, how it's served, and, for a
// remux or a transcode, its Maker's ID, what was made, Overfin's own
// master playlist and the media playlist made. A file's real path is kept
// in memory only, for its requests (never logged, never sent).
type Started struct {
	Plan     *Plan
	Protocol string // wire.ProtocolFile or wire.ProtocolHLS
	ID       string
	Made     *Made
	Master   []byte
	file     *opened
}

// opened is what a file delivery found when it started.
type opened struct {
	real      string
	size, mod int64
}

// Method is the play answer's method.
func (s *Started) Method() string { return s.Plan.Method() }

// Transcodes says whether it takes a transcode slot: its video is
// converted.
func (s *Started) Transcodes() bool { return s.Plan.VideoConverted }

// JellyfinIndex is Jellyfin's index of one of the copy's tracks (a<n>,
// s<n>), for the plugin's calls; false when it isn't mapped.
func (s *Started) JellyfinIndex(track string) (int, bool) {
	n, ok := s.Plan.Copy.Streams[track]
	return n, ok
}

// notApart are the subtitle formats never served apart (rule 17): DVD
// and DVB pictures.
var notApart = []string{"dvd_subtitle", "dvb_subtitle"}

// SubtitleServing is how this server serves a subtitle track of a copy
// apart, to its own players as to its peers: as MSFP serves its format
// (wire.ServeSubtitle), never DVD or DVB pictures (rule 17), nor a track
// Jellyfin's numbering doesn't map (mapped false).
func SubtitleServing(format string, mapped bool) wire.SubtitleServing {
	return wire.ServeSubtitle(format, "", !mapped || slices.Contains(notApart, format))
}

// SubtitleServing is how the copy's subtitle track id is served apart
// (SubtitleServing); none for a track it hasn't.
func (s *Started) SubtitleServing(id string) wire.SubtitleServing {
	t, ok := s.Subtitle(id)
	if !ok {
		return wire.SubtitleServing{}
	}
	_, mapped := s.JellyfinIndex(id)
	return SubtitleServing(t.Format, mapped)
}

// Subtitle is one of the copy's subtitle tracks, by its ID.
func (s *Started) Subtitle(id string) (wire.CopySubtitle, bool) {
	for _, t := range s.Plan.Copy.Facts.SubtitleTracks {
		if t.ID == id {
			return t, true
		}
	}
	return wire.CopySubtitle{}, false
}

// Start makes what p planned. The caller has counted the playback
// first. A file is checked to be the copy (its fingerprint, its size); a
// remux or a transcode is made by the Maker, and what it made checked
// against the order: any difference stops it and refuses the play.
func (e *Executor) Start(ctx context.Context, p *Plan) (*Started, error) {
	if p.Kind == KindFile {
		f, err := e.openCopy(ctx, p.Copy)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return &Started{Plan: p, Protocol: wire.ProtocolFile,
			file: &opened{real: f.Real, size: f.Info.Size(), mod: f.Info.ModTime().UnixNano()}}, nil
	}
	if e.Maker == nil || p.order == nil {
		return nil, refused("no-maker")
	}
	o := *p.order
	m, err := e.Maker.Start(ctx, o)
	if err != nil {
		return nil, err
	}
	if diff := Differences(o, p.Copy.Facts, m); len(diff) > 0 {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopTime)
		defer cancel()
		_ = e.Maker.Stop(stopCtx, m.ID)
		return nil, refused("made-differs:" + strings.Join(diff, ","))
	}
	return &Started{Plan: p, Protocol: wire.ProtocolHLS, ID: m.ID, Made: m, Master: Master(p.Copy.Facts, p.Audio, o, m)}, nil
}

// openCopy opens a copy's file through the path checks, and checks it is
// still the file the copy describes.
func (e *Executor) openCopy(ctx context.Context, c store.IndexedCopy) (*files.File, error) {
	path, err := e.Path(ctx, c.ItemID, c.SourceID)
	if err != nil {
		return nil, refused("file-unknown")
	}
	libs, err := e.Libraries(ctx)
	if err != nil {
		return nil, busy("libraries-unknown")
	}
	f, err := libs.Open(path)
	if err != nil {
		return nil, refused("file-refused")
	}
	fp, err := e.Fingerprint(ctx, c.ItemID, c.SourceID, f)
	if err != nil || fp != c.Fingerprint || f.Info.Size() != c.Size {
		f.Close()
		return nil, refused("file-changed")
	}
	return f, nil
}

// Stop ends a delivery at its Maker; a file has nothing to stop.
func (e *Executor) Stop(ctx context.Context, s *Started) error {
	if s == nil || s.ID == "" || e.Maker == nil {
		return nil
	}
	return e.Maker.Stop(ctx, s.ID)
}

// Ping keeps a delivery alive at its Maker; ErrGone when it's over.
func (e *Executor) Ping(ctx context.Context, s *Started) error {
	if s == nil || s.ID == "" || e.Maker == nil {
		return nil
	}
	return e.Maker.Ping(ctx, s.ID)
}

// Clear ends every delivery at the Maker (Overfin starting).
func (e *Executor) Clear(ctx context.Context) error {
	if e.Maker == nil {
		return nil
	}
	return e.Maker.Clear(ctx)
}

// Differences are what a Maker made otherwise than ordered, as closed
// codes: a stream copied or not, another codec, a picture lower than
// asked (Jellyfin's sizes by bitrate; only when a height was asked) or higher, a bitrate above the one
// asked (below is fine: Jellyfin caps it near the copy's own), tone
// mapped or not, another audio stream, more channels, other segments.
func Differences(o Order, c wire.Copy, m *Made) []string {
	var d []string
	if m == nil {
		return []string{"nothing-made"}
	}
	if v := o.Video; v != nil {
		switch {
		case v.Copy && !m.VideoCopied:
			d = append(d, "video-converted")
		case !v.Copy && m.VideoCopied:
			d = append(d, "video-copied")
		case !v.Copy:
			if m.VideoCodec != v.Codec {
				d = append(d, "video-codec")
			}
			want := v.MaxHeight
			if c.Video != nil && c.Video.Height > 0 {
				want = min(want, c.Video.Height)
			}
			switch {
			case m.Height > want:
				d = append(d, "height-over")
			case v.HeightAsked && m.Height > 0 && m.Height < want-2: // even sizes: 1 or 2 lines less
				d = append(d, "height-cap")
			}
			if m.VideoBitrate > v.MaxBitrate {
				d = append(d, "bitrate-over")
			}
			if m.ToneMapped != v.ToneMap {
				if m.ToneMapped {
					d = append(d, "tone-mapped")
				} else {
					d = append(d, "not-tone-mapped")
				}
			}
		}
	}
	if a := o.Audio; a != nil {
		switch {
		case m.AudioIndex != a.Index:
			d = append(d, "audio-stream")
		case a.Copy && !m.AudioCopied:
			d = append(d, "audio-converted")
		case !a.Copy && m.AudioCopied:
			d = append(d, "audio-copied")
		case !a.Copy && m.AudioCodec != a.Codec:
			d = append(d, "audio-codec")
		case !a.Copy && m.AudioChannels > a.Channels:
			d = append(d, "audio-channels")
		}
	}
	if m.Segments != o.Segments {
		d = append(d, "segments")
	}
	if !strings.Contains(string(m.Playlist), "#EXT-X-ENDLIST") || !strings.HasPrefix(string(m.Playlist), "#EXTM3U") {
		d = append(d, "playlist")
	}
	return d
}

// errOver is a file's read once its playback is over.
var errOver = errors.New("delivery: the playback is over")

// stopTime bounds stopping a delivery made otherwise than ordered.
const stopTime = 15 * time.Second
