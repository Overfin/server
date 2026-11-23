package delivery

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/msfp-io/msfp-go/wire"
	"github.com/overfin/server/internal/jellyfin"
)

// PluginMaker is the Maker of Jellyfin's own transcoder, through
// Overfin's companion plugin (0.9.0 and later): the plugin starts
// Jellyfin's HLS for the order's closed values, reads Jellyfin's ffmpeg
// command line as soon as it runs, refuses what differs from the order,
// keeps it alive while pinged, and stops it after two minutes without a
// segment read or a ping (its watchdog).
type PluginMaker struct {
	Jellyfin *jellyfin.Client
	// Admin is Overfin's key for its plugin.
	Admin func() jellyfin.Auth
	// EncodersFor is how long the plugin's encoders are kept.
	EncodersFor time.Duration

	mu      sync.Mutex
	known   *Encoders
	knownAt time.Time
}

// pluginCodecs are the codecs Overfin asks the plugin for, as the plugin
// names them: registry tokens.
var (
	pluginVideo = []string{"h264", "hevc", "av1"}
	pluginAudio = []string{"aac", "ac3", "eac3", "mp3", "opus", "flac"}
)

// Encoders are the plugin's, kept a while.
func (p *PluginMaker) Encoders(ctx context.Context) (Encoders, error) {
	p.mu.Lock()
	keep := p.EncodersFor
	if keep <= 0 {
		keep = 10 * time.Minute
	}
	if p.known != nil && time.Since(p.knownAt) < keep {
		e := *p.known
		p.mu.Unlock()
		return e, nil
	}
	p.mu.Unlock()
	got, err := p.Jellyfin.Encoders(ctx, p.Admin())
	if err != nil {
		return Encoders{}, err
	}
	// Only the codecs Overfin knows, each once: the plugin's list is read
	// like any upstream answer.
	e := Encoders{Hardware: got.Hardware, ToneMapping: got.ToneMapping}
	for _, c := range got.Video {
		if slices.Contains(pluginVideo, c) && !slices.Contains(e.Video, c) {
			e.Video = append(e.Video, c)
		}
	}
	for _, c := range got.Audio {
		if slices.Contains(pluginAudio, c) && !slices.Contains(e.Audio, c) {
			e.Audio = append(e.Audio, c)
		}
	}
	// Jellyfin keeps HDR while converting only on a GPU path, which the
	// spike couldn't test: not offered until it is.
	e.KeepsHDR = false
	p.mu.Lock()
	p.known, p.knownAt = &e, time.Now()
	p.mu.Unlock()
	return e, nil
}

// Start asks the plugin for the order.
func (p *PluginMaker) Start(ctx context.Context, o Order) (*Made, error) {
	jo := jellyfin.DeliveryOrder{ItemId: o.ItemID, SourceId: o.SourceID, Segments: o.Segments, StartTicks: o.StartMs * 10_000}
	if v := o.Video; v != nil {
		jo.Video = &jellyfin.DeliveryVideo{Copy: v.Copy, Codec: v.Codec, MaxHeight: v.MaxHeight, MaxBitrate: v.MaxBitrate, ToneMap: v.ToneMap}
	}
	if a := o.Audio; a != nil {
		jo.Audio = &jellyfin.DeliveryAudio{Index: a.Index, Copy: a.Copy, Codec: a.Codec, Channels: a.Channels}
	}
	m, err := p.Jellyfin.StartDelivery(ctx, p.Admin(), jo)
	var r *jellyfin.DeliveryRefused
	switch {
	case errors.As(err, &r):
		if r.Code == "busy" {
			return nil, busy("maker-busy")
		}
		return nil, refused(r.Code)
	case err != nil:
		return nil, busy("maker-failed")
	}
	if len(m.Playlist) > wire.MaxPlaylist {
		_ = p.Stop(context.WithoutCancel(ctx), m.Id)
		return nil, refused("playlist-too-large")
	}
	made := &Made{ID: m.Id, VideoCopied: m.VideoCopied, VideoCodec: m.VideoCodec, Width: m.Width, Height: m.Height,
		VideoBitrate: m.VideoBitrate, ToneMapped: m.ToneMapped, AudioCopied: m.AudioCopied, AudioCodec: m.AudioCodec,
		AudioChannels: m.AudioChannels, AudioIndex: m.AudioIndex, Segments: m.Segments, Playlist: []byte(m.Playlist)}
	if made.VideoCopied {
		made.VideoCodec = ""
	}
	if made.AudioCopied {
		made.AudioCodec = ""
	}
	return made, nil
}

// Ping keeps a delivery alive.
func (p *PluginMaker) Ping(ctx context.Context, id string) error {
	err := p.Jellyfin.PingDelivery(ctx, p.Admin(), id)
	if jellyfin.IsStatus(err, http.StatusNotFound) {
		return ErrGone
	}
	return err
}

// Stop stops a delivery.
func (p *PluginMaker) Stop(ctx context.Context, id string) error {
	return p.Jellyfin.StopDelivery(ctx, p.Admin(), id)
}

// Clear stops every delivery.
func (p *PluginMaker) Clear(ctx context.Context) error {
	return p.Jellyfin.ClearDeliveries(ctx, p.Admin())
}
