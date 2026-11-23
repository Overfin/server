// Package delivery carries out what a play asks of a copy (MSFP playback
// sections 2.2 and 2.7, Overfin's plan section 4.2): the file itself from
// disk, a remux or a transcode made by a Maker (Jellyfin's transcoder
// through Overfin's companion plugin today; Overfin's own ffmpeg later),
// or the baseline, each stream already within it left unchanged.
//
// One executor serves Overfin's own players and paired servers' alike.
// It decides what is made (Plan), and only then, once the caller has
// counted the playback (a stream, or a transcode when the video is
// converted), makes it (Start), checks what was made against what was
// asked (a difference is a refusal, never a silent re-encode), and writes
// Overfin's own master playlist. It never takes a path, a URL or a
// command from a request: only closed values and registry tokens.
package delivery

import (
	"context"
	"errors"

	"github.com/msfp-io/msfp-go/wire"
)

// The kinds of delivery made (MSFP's deliveries; the baseline is made as
// a remux or a transcode, as wire.Serve says).
const (
	KindFile      = "file"
	KindRemux     = "remux"
	KindTranscode = "transcode"
)

// Segment formats a Maker writes (HLS): MPEG-TS or fragmented MP4.
const (
	SegmentsTS  = "ts"
	SegmentsMP4 = "mp4"
)

// Maker makes remuxes and transcodes. The companion plugin is its first
// implementation (PluginMaker); Overfin's own ffmpeg will be the second.
// A Maker takes only IDs and closed values (Order) and answers IDs,
// closed values and the media playlist it made, never a path.
type Maker interface {
	// Encoders are what it can make now.
	Encoders(ctx context.Context) (Encoders, error)
	// Start makes an order: its ID, what it really made, and its media
	// playlist (complete, VOD). ErrRefused (a *Refusal) when it won't or
	// can't make exactly that.
	Start(ctx context.Context, o Order) (*Made, error)
	// Ping keeps a delivery alive while its player is (a paused player
	// reads nothing); ErrGone when it's over there.
	Ping(ctx context.Context, id string) error
	// Stop ends a delivery; one already gone is no error.
	Stop(ctx context.Context, id string) error
	// Clear ends every delivery: at Overfin's start, those of a run that
	// ended without stopping them.
	Clear(ctx context.Context) error
}

// Encoders are what a Maker makes: the codecs it encodes (registry
// tokens), whether it maps HDR to SDR (tone mapping), whether it keeps
// HDR while converting (a hardware path; never on the CPU), and whether
// it encodes on hardware.
type Encoders struct {
	Video       []string
	Audio       []string
	ToneMapping bool
	KeepsHDR    bool
	Hardware    bool
}

// Wire is the encoders as MSFP's Serve reads them: the codecs, and the
// ranges made of a converted picture (sdr, only with tone mapping; keep,
// only where HDR is kept).
func (e Encoders) Wire() wire.Encoders {
	w := wire.Encoders{Video: e.Video, Audio: e.Audio}
	if e.KeepsHDR {
		w.Ranges = []string{wire.RangeKeep}
	}
	return w
}

// Order is a remux or a transcode as a Maker is asked for it: Jellyfin's
// version (item and source IDs, never leaving Overfin), the segments, the
// picture (copied, or converted to a codec, a height, a bitrate, tone
// mapped or not) and the audio stream (Jellyfin's index; copied, or
// converted to a codec and channels), and where to start.
type Order struct {
	ItemID, SourceID string
	Segments         string
	Video            *VideoOrder // nil: no picture (a copy with no video)
	Audio            *AudioOrder // nil: no audio
	StartMs          int64
}

// VideoOrder is an order's picture: Copy, or converted to Codec (h264,
// hevc, av1), at most MaxHeight lines (the copy's own when lower) and
// MaxBitrate bits a second, ToneMap mapping an HDR picture to SDR.
type VideoOrder struct {
	Copy       bool
	Codec      string
	MaxHeight  int
	MaxBitrate int
	ToneMap    bool
}

// AudioOrder is an order's audio: Jellyfin's index of the stream, Copy,
// or converted to Codec with at most Channels channels.
type AudioOrder struct {
	Index    int
	Copy     bool
	Codec    string
	Channels int
}

// Made is what a Maker made of an order, as it read it once making began
// (Jellyfin's ffmpeg command line, for the plugin): the delivery's ID,
// and closed values only. Playlist is the media playlist, its segments'
// URIs relative to the version's fixed playlist path.
type Made struct {
	ID            string
	VideoCopied   bool
	VideoCodec    string // h264, hevc, av1; "" when copied
	Width, Height int    // of a converted picture; 0 when not known
	VideoBitrate  int    // its cap, bits a second; 0 when not known
	ToneMapped    bool
	AudioCopied   bool
	AudioCodec    string // "" when copied
	AudioChannels int
	AudioIndex    int // Jellyfin's index of the stream; -1 for none
	Segments      string
	Playlist      []byte
}

// Refusal is a delivery refused: an MSFP problem and its HTTP status (409
// delivery-refused, 503 server-busy), and a closed code saying why, for
// the logs and the player's information (never free text from upstream).
type Refusal struct {
	Status  int
	Problem string
	Why     string
}

func (r *Refusal) Error() string { return "delivery: " + r.Problem + ": " + r.Why }

// ErrRefused matches every Refusal (errors.Is).
var ErrRefused = errors.New("delivery: refused")

func (r *Refusal) Is(target error) bool { return target == ErrRefused }

// ErrGone is a delivery over at its Maker (stopped by its watchdog, or
// by Jellyfin).
var ErrGone = errors.New("delivery: gone")

func refused(why string) *Refusal {
	return &Refusal{Status: 409, Problem: wire.ProblemDeliveryRefused, Why: why}
}

func busy(why string) *Refusal {
	return &Refusal{Status: 503, Problem: wire.ProblemServerBusy, Why: why}
}

// AsRefusal is err as a Refusal: a Refusal as it is, an MSFP refusal of
// wire.Serve with its status and problem; nil for any other error.
func AsRefusal(err error) *Refusal {
	var r *Refusal
	if errors.As(err, &r) {
		return r
	}
	var w *wire.PlayRefusal
	if errors.As(err, &w) {
		return &Refusal{Status: w.Status, Problem: w.Problem, Why: w.Why}
	}
	return nil
}
