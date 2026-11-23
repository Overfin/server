package delivery

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/msfp-io/msfp-go/wire"
	"github.com/overfin/server/internal/files"
	"github.com/overfin/server/internal/store"
)

// fakeMaker makes what it's told: by default exactly the order, else
// what made says.
type fakeMaker struct {
	enc     Encoders
	encErr  error
	orders  []Order
	stopped []string
	pinged  []string
	made    func(Order) *Made
	err     error
	cleared bool
}

func (f *fakeMaker) Encoders(context.Context) (Encoders, error) { return f.enc, f.encErr }

func (f *fakeMaker) Start(_ context.Context, o Order) (*Made, error) {
	f.orders = append(f.orders, o)
	if f.err != nil {
		return nil, f.err
	}
	if f.made != nil {
		return f.made(o), nil
	}
	return exactly(o), nil
}

func (f *fakeMaker) Ping(_ context.Context, id string) error {
	f.pinged = append(f.pinged, id)
	return nil
}

func (f *fakeMaker) Stop(_ context.Context, id string) error {
	f.stopped = append(f.stopped, id)
	return nil
}

func (f *fakeMaker) Clear(context.Context) error {
	f.cleared = true
	return nil
}

// exactly is what a Maker makes of an order when it obeys: the height
// asked (the fixtures' copies are taller), the bitrate asked.
func exactly(o Order) *Made {
	m := &Made{ID: strings.Repeat("a", 32), Segments: o.Segments, AudioIndex: -1,
		Playlist: []byte("#EXTM3U\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXTINF:6.0,\nhls1/main/0.ts\n#EXT-X-ENDLIST\n")}
	if v := o.Video; v != nil {
		m.VideoCopied = v.Copy
		if !v.Copy {
			m.VideoCodec, m.Height, m.Width, m.VideoBitrate, m.ToneMapped = v.Codec, v.MaxHeight, v.MaxHeight*16/9, v.MaxBitrate, v.ToneMap
		}
	}
	if a := o.Audio; a != nil {
		m.AudioIndex, m.AudioCopied = a.Index, a.Copy
		if !a.Copy {
			m.AudioCodec, m.AudioChannels = a.Codec, a.Channels
		}
	}
	return m
}

func ptr[T any](v T) *T { return &v }

// sdrCopy is an H.264 1080p film with E-AC-3 5.1 and AAC stereo.
func sdrCopy() store.IndexedCopy {
	one := ptr(1)
	return store.IndexedCopy{CopyID: "v" + strings.Repeat("a", 26), ItemID: strings.Repeat("1", 32), SourceID: strings.Repeat("1", 32),
		ETag: "etagetagetagetagetaget", Fingerprint: "x", Size: 6_000_000_000,
		Facts: wire.Copy{DurationMs: 6_000_000, MediaType: "video/matroska", Size: 6_000_000_000,
			Video: &wire.VideoStream{Codec: "h264", Codecs: "avc1.640028", Width: 1920, Height: 1080, BitDepth: 8,
				Colour: &wire.VideoColour{Primaries: one, Transfer: one, Matrix: one}},
			AudioTracks: []wire.CopyAudio{
				{ID: "a1", Codec: "eac3", Codecs: "ec-3", Channels: 6, Default: true, Language: "en", SampleRate: 48000},
				{ID: "a2", Codec: "aac", Codecs: "mp4a.40.2", Channels: 2, Language: "fr", SampleRate: 48000},
			},
			SubtitleTracks: []wire.CopySubtitle{{ID: "s3", Format: "subrip", Language: "fr"}}},
		Streams: map[string]int{"v": 0, "a1": 1, "a2": 2, "s3": 3}}
}

// hdrCopy is a 4K HEVC Main 10 HDR10 film, or Dolby Vision 8.4 (HLG
// base) with dv.
func hdrCopy(dv bool) store.IndexedCopy {
	c := sdrCopy()
	transfer := wire.TransferPQ
	if dv {
		transfer = wire.TransferHLG
	}
	c.Facts.Video = &wire.VideoStream{Codec: "hevc", Codecs: "hev1.2.4.L153.B0", Width: 3840, Height: 2160, BitDepth: 10,
		Colour: &wire.VideoColour{Primaries: ptr(9), Transfer: ptr(transfer), Matrix: ptr(9)}}
	if dv {
		c.Facts.Video.DV = &wire.VideoDV{Profile: 8, Level: 4, Compatibility: 4}
	}
	return c
}

var allEncoders = Encoders{Video: []string{"h264", "hevc", "av1"}, Audio: []string{"aac", "ac3", "eac3"}, ToneMapping: true}

func plan(t *testing.T, e *Executor, c store.IndexedCopy, r Request) *Plan {
	t.Helper()
	p, err := e.Plan(context.Background(), c, r)
	if err != nil {
		t.Fatalf("%+v: %v", r.Play, err)
	}
	return p
}

func refusedWith(t *testing.T, err error, status int, why string) {
	t.Helper()
	r := AsRefusal(err)
	if r == nil || r.Status != status || !strings.HasPrefix(r.Why, why) {
		t.Errorf("refusal %v, want %d %s", err, status, why)
	}
}

// What each request makes: the kind, what is converted, what the Maker
// is asked; a remux or an audio-only conversion takes no transcode slot,
// a converted picture does.
func TestPlan(t *testing.T) {
	m := &fakeMaker{enc: allEncoders}
	e := &Executor{Maker: m, Path: func(context.Context, string, string) (string, error) { return "", nil },
		Libraries:   func(context.Context) (files.Libraries, error) { return files.Libraries{}, nil },
		Fingerprint: func(context.Context, string, string, *files.File) (string, error) { return "", nil }}
	c := sdrCopy()

	p := plan(t, e, c, Request{Play: wire.PlayRequest{Delivery: wire.DeliveryFile}, Own: true})
	if p.Kind != KindFile || p.Method() != wire.MethodDirect || p.VideoConverted || p.Order() != nil {
		t.Errorf("file: %+v", p)
	}

	p = plan(t, e, c, Request{Play: wire.PlayRequest{Delivery: wire.DeliveryRemux, MediaType: "video/mp4", AudioTrack: "a2"}})
	if o := p.Order(); p.Kind != KindRemux || p.Method() != wire.MethodRemux || p.VideoConverted || p.AudioConverted ||
		o.Segments != SegmentsMP4 || !o.Video.Copy || !o.Audio.Copy || o.Audio.Index != 2 {
		t.Errorf("remux: %+v %+v", p, p.Order())
	}

	// Video copied, audio converted: a transcode by method, a stream by count.
	p = plan(t, e, c, Request{Play: wire.PlayRequest{Delivery: wire.DeliveryTranscode,
		Transcode: &wire.TranscodeTargets{VideoCodec: wire.CodecCopy, AudioCodec: "aac", AudioChannels: 6}}})
	if o := p.Order(); p.Method() != wire.MethodTranscode || p.VideoConverted || !p.AudioConverted || !o.Video.Copy ||
		o.Audio.Copy || o.Audio.Codec != "aac" || o.Audio.Channels != 6 || o.Audio.Index != 1 || o.Segments != SegmentsTS {
		t.Errorf("audio only: %+v %+v %+v", p, p.Order().Video, p.Order().Audio)
	}

	// The picture converted: a transcode slot; the height and bitrate
	// asked, the cap's share for the audio taken off.
	p = plan(t, e, c, Request{Play: wire.PlayRequest{Delivery: wire.DeliveryTranscode, StartPositionMs: 60_000,
		Transcode: &wire.TranscodeTargets{VideoCodec: "hevc", MaxHeight: 720, MaxBitrateKbps: 6000}}, CapKbps: 5000})
	if o := p.Order(); !p.VideoConverted || o.Video.Codec != "hevc" || o.Video.MaxHeight != 720 || o.Video.MaxBitrate != (5000-192)*1000 ||
		o.Video.ToneMap || o.Segments != SegmentsMP4 || o.StartMs != 60_000 || o.Audio.Codec != "aac" || o.Audio.Channels != 2 {
		t.Errorf("transcode: %+v %+v %+v", p, p.Order().Video, p.Order().Audio)
	}
	// Taller than the copy: the copy's height.
	p = plan(t, e, c, Request{Play: wire.PlayRequest{Delivery: wire.DeliveryTranscode, Transcode: &wire.TranscodeTargets{MaxHeight: 2160}}})
	if o := p.Order(); o.Video.MaxHeight != 1080 || o.Video.Codec != "h264" || o.Video.MaxBitrate != 16_000_000 {
		t.Errorf("above the copy: %+v", o.Video)
	}

	// The baseline: the H.264 picture within it copied, E-AC-3 converted.
	p = plan(t, e, c, Request{})
	if o := p.Order(); p.VideoConverted || !p.AudioConverted || !o.Video.Copy || o.Audio.Codec != "aac" || o.Segments != SegmentsTS {
		t.Errorf("baseline: %+v %+v", p, p.Order().Audio)
	}

	// HDR to SDR: tone mapped; HDR kept: refused on the CPU (Serve: no
	// such range made).
	h := hdrCopy(false)
	p = plan(t, e, h, Request{})
	if o := p.Order(); !p.VideoConverted || !o.Video.ToneMap || o.Video.MaxHeight != 2160 {
		t.Errorf("HDR baseline: %+v", p.Order().Video)
	}
	_, err := e.Plan(context.Background(), h, Request{Play: wire.PlayRequest{Delivery: wire.DeliveryTranscode,
		Transcode: &wire.TranscodeTargets{VideoCodec: "hevc", Range: wire.RangeKeep}}})
	refusedWith(t, err, 409, "a range")
	m.enc.ToneMapping = false
	_, err = e.Plan(context.Background(), h, Request{})
	refusedWith(t, err, 409, "tone-mapping-off")
	m.enc.ToneMapping = true

	// A picture Overfin hasn't read: converted to the baseline; never
	// copied, nor its file served.
	mc := c
	mc.Minimal, mc.Facts.Video = true, nil
	mc.Facts.AudioTracks = []wire.CopyAudio{{ID: "a1", Codec: "eac3", Channels: 6}}
	p = plan(t, e, mc, Request{})
	if o := p.Order(); !p.VideoConverted || o.Video.Codec != "h264" || o.Video.MaxHeight != 1080 || o.Audio.Codec != "aac" {
		t.Errorf("minimal: %+v %+v", p.Order().Video, p.Order().Audio)
	}
	for _, r := range []wire.PlayRequest{{Delivery: wire.DeliveryFile}, {Delivery: wire.DeliveryRemux, MediaType: "video/mp4"},
		{Delivery: wire.DeliveryTranscode, Transcode: &wire.TranscodeTargets{VideoCodec: wire.CodecCopy}}} {
		_, err := e.Plan(context.Background(), mc, Request{Play: r})
		refusedWith(t, err, 409, "")
	}
}

// What's refused before anything starts.
func TestPlanRefusals(t *testing.T) {
	m := &fakeMaker{enc: allEncoders}
	off := false
	e := &Executor{Maker: m, OriginalFile: func() bool { return off },
		Path:        func(context.Context, string, string) (string, error) { return "", nil },
		Libraries:   func(context.Context) (files.Libraries, error) { return files.Libraries{}, nil },
		Fingerprint: func(context.Context, string, string, *files.File) (string, error) { return "", nil }}
	c := sdrCopy()
	ctx := context.Background()
	cases := []struct {
		name   string
		c      store.IndexedCopy
		r      Request
		status int
		why    string
	}{
		{"burned, own", c, Request{Own: true, Play: wire.PlayRequest{Delivery: wire.DeliveryTranscode,
			Transcode: &wire.TranscodeTargets{BurnSubtitle: "s3"}}}, 409, "burned-subtitles"},
		{"burned, peer", c, Request{Play: wire.PlayRequest{Delivery: wire.DeliveryTranscode,
			Transcode: &wire.TranscodeTargets{BurnSubtitle: "s3"}}}, 409, "burn-in-off"},
		{"original off", c, Request{Own: true, Play: wire.PlayRequest{Delivery: wire.DeliveryFile}}, 409, "original-file-off"},
		{"over the cap", c, Request{Play: wire.PlayRequest{Delivery: wire.DeliveryRemux, MediaType: "video/mp4"}, CapKbps: 4000}, 409, "the copy's overall rate"},
		{"truehd in ts", func() store.IndexedCopy {
			x := sdrCopy()
			x.Facts.AudioTracks[0].Codec = "truehd"
			return x
		}(), Request{Play: wire.PlayRequest{Delivery: wire.DeliveryRemux, MediaType: "video/mp2t"}}, 409, "a remux"},
		{"no encoder", c, Request{Play: wire.PlayRequest{Delivery: wire.DeliveryTranscode,
			Transcode: &wire.TranscodeTargets{VideoCodec: "vp9"}}}, 409, "a video codec"},
		{"unmapped track", func() store.IndexedCopy {
			x := sdrCopy()
			delete(x.Streams, "a1")
			return x
		}(), Request{}, 409, "track-not-mapped"},
	}
	for _, tc := range cases {
		_, err := e.Plan(ctx, tc.c, tc.r)
		if r := AsRefusal(err); r == nil || r.Status != tc.status || !strings.HasPrefix(r.Why, tc.why) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	// The file for a paired server's viewer whatever the switch (its
	// pairing's limits govern it).
	if _, err := e.Plan(ctx, c, Request{Play: wire.PlayRequest{Delivery: wire.DeliveryFile}}); err != nil {
		t.Errorf("a peer's file: %v", err)
	}
	m.encErr = errors.New("down")
	_, err := e.Plan(ctx, c, Request{})
	refusedWith(t, err, 503, "encoders-unknown")
}

// A player asking only a bitrate (a lower quality) takes Jellyfin's own
// size for it: a picture lower than the copy's isn't a difference.
func TestBitrateOnlyTakesJellyfinsSize(t *testing.T) {
	ctx := context.Background()
	m := &fakeMaker{enc: allEncoders}
	e := &Executor{Maker: m}
	ask := Request{Play: wire.PlayRequest{Delivery: wire.DeliveryTranscode, MaxBitrateKbps: 1500,
		Transcode: &wire.TranscodeTargets{VideoCodec: "h264", MaxBitrateKbps: 1000}}}
	m.made = func(o Order) *Made {
		made := exactly(o)
		made.Height, made.Width = 720, 1280 // Jellyfin's size for 1 Mbit/s
		return made
	}
	if _, err := e.Start(ctx, plan(t, e, sdrCopy(), ask)); err != nil {
		t.Fatalf("a bitrate-only play refused: %v", err)
	}
}

// What's made is checked: a difference stops the delivery and refuses
// the play; a bitrate below the one asked is fine.
func TestStartChecksWhatWasMade(t *testing.T) {
	ctx := context.Background()
	m := &fakeMaker{enc: allEncoders}
	e := &Executor{Maker: m}
	c := sdrCopy()
	ask := Request{Play: wire.PlayRequest{Delivery: wire.DeliveryTranscode, Transcode: &wire.TranscodeTargets{MaxHeight: 1080, MaxBitrateKbps: 1000}}}

	m.made = func(o Order) *Made {
		made := exactly(o)
		made.Height, made.Width = 540, 960 // Jellyfin's sizes by bitrate
		return made
	}
	_, err := e.Start(ctx, plan(t, e, c, ask))
	refusedWith(t, err, 409, "made-differs:height-cap")
	if len(m.stopped) != 1 {
		t.Errorf("stopped: %v", m.stopped)
	}

	m.made = func(o Order) *Made {
		made := exactly(o)
		made.VideoBitrate = o.Video.MaxBitrate / 2
		return made
	}
	s, err := e.Start(ctx, plan(t, e, c, ask))
	if err != nil || s.Protocol != wire.ProtocolHLS || s.ID == "" || !s.Transcodes() {
		t.Fatalf("a lower bitrate: %v %+v", err, s)
	}
	for _, want := range []string{"#EXT-X-STREAM-INF:", `CODECS="avc1.640028,mp4a.40.2"`, "RESOLUTION=1920x1080", "VIDEO-RANGE=SDR", "\nv0.m3u8\n"} {
		if !strings.Contains(string(s.Master), want) {
			t.Errorf("master lacks %s:\n%s", want, s.Master)
		}
	}
	if err := e.Ping(ctx, s); err != nil || len(m.pinged) != 1 {
		t.Errorf("ping: %v", err)
	}
	if err := e.Stop(ctx, s); err != nil || len(m.stopped) != 2 {
		t.Errorf("stop: %v", err)
	}

	for name, change := range map[string]func(*Made){
		"video-copied":   func(made *Made) { made.VideoCopied = true },
		"tone-mapped":    func(made *Made) { made.ToneMapped = true },
		"audio-stream":   func(made *Made) { made.AudioIndex = 2 },
		"audio-channels": func(made *Made) { made.AudioChannels = 6 },
		"segments":       func(made *Made) { made.Segments = SegmentsMP4 },
		"playlist":       func(made *Made) { made.Playlist = []byte("#EXTM3U\n#EXT-X-PLAYLIST-TYPE:EVENT\n") },
		"bitrate-over":   func(made *Made) { made.VideoBitrate *= 2 },
		"video-codec":    func(made *Made) { made.VideoCodec = "hevc" },
	} {
		m.made = func(o Order) *Made {
			made := exactly(o)
			change(made)
			return made
		}
		_, err := e.Start(ctx, plan(t, e, c, ask))
		refusedWith(t, err, 409, "made-differs:"+name)
	}

	// The Maker's own refusal passes on as it is.
	m.made, m.err = nil, refused("hdr-kept-needs-gpu")
	_, err = e.Start(ctx, plan(t, e, c, ask))
	refusedWith(t, err, 409, "hdr-kept-needs-gpu")
	if err := e.Clear(ctx); err != nil || !m.cleared {
		t.Error("clear")
	}
}

// Overfin's master playlist for a Dolby Vision copy remuxed in
// fragmented MP4: hvc1 with the Dolby Vision entry beside it, HLG kept.
func TestMasterDolbyVision(t *testing.T) {
	c := hdrCopy(true)
	o := Order{Segments: SegmentsMP4, Video: &VideoOrder{Copy: true}, Audio: &AudioOrder{Index: 1, Copy: true}}
	got := string(Master(c.Facts, "a1", o, exactly(o)))
	for _, want := range []string{"#EXT-X-VERSION:7", `CODECS="hvc1.2.4.L153.B0,ec-3"`, `SUPPLEMENTAL-CODECS="dvh1.08.04/db4h"`,
		"VIDEO-RANGE=HLG", "RESOLUTION=3840x2160", "BANDWIDTH=8000000"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %s:\n%s", want, got)
		}
	}
	// HDR10 tone mapped to SDR, as H.264 720p.
	h := hdrCopy(false)
	o = Order{Segments: SegmentsTS, Video: &VideoOrder{Codec: "h264", MaxHeight: 720, MaxBitrate: 3_000_000, ToneMap: true},
		Audio: &AudioOrder{Index: 1, Codec: "aac", Channels: 2}}
	got = string(Master(h.Facts, "a1", o, exactly(o)))
	for _, want := range []string{"#EXT-X-VERSION:3", `CODECS="avc1.64001f,mp4a.40.2"`, "VIDEO-RANGE=SDR", "BANDWIDTH=3192000"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "SUPPLEMENTAL") {
		t.Errorf("supplemental codecs on a converted picture:\n%s", got)
	}
}

// A file served from disk: only a file the copy describes, in the
// libraries; by ranges; never once it changed, nor after its playback.
func TestFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	lib := filepath.Join(dir, "films")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(lib, "a.mkv")
	data := []byte("0123456789abcdefghij")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(dir, "secret.mkv")
	os.WriteFile(secret, data, 0o644)
	os.Symlink(secret, filepath.Join(lib, "link.mkv"))
	where := path
	e := &Executor{
		Path:        func(context.Context, string, string) (string, error) { return where, nil },
		Libraries:   func(context.Context) (files.Libraries, error) { return files.NewLibraries([]string{lib}), nil },
		Fingerprint: func(_ context.Context, _, _ string, f *files.File) (string, error) { return files.Fingerprint(f) },
	}
	c := sdrCopy()
	f, _ := files.NewLibraries([]string{lib}).Open(path)
	c.Fingerprint, _ = files.Fingerprint(f)
	f.Close()
	c.Size, c.Facts.Size, c.Facts.DurationMs = int64(len(data)), int64(len(data)), 1000

	p := plan(t, e, c, Request{Own: true, Play: wire.PlayRequest{Delivery: wire.DeliveryFile}})
	s, err := e.Start(ctx, p)
	if err != nil || s.Protocol != wire.ProtocolFile || s.Transcodes() {
		t.Fatalf("start: %v %+v", err, s)
	}
	done := make(chan struct{})
	get := func(rangeHeader string) *httptest.ResponseRecorder {
		t.Helper()
		f, err := e.OpenFile(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		if rangeHeader != "" {
			r.Header.Set("Range", rangeHeader)
		}
		w := httptest.NewRecorder()
		ServeFile(w, r, s, f, done)
		return w
	}
	w := get("")
	if w.Code != 200 || w.Body.String() != string(data) || w.Header().Get("Content-Type") != "video/matroska" ||
		w.Header().Get("ETag") != `"etagetagetagetagetaget"` || w.Header().Get("Last-Modified") != "" {
		t.Errorf("whole: %d %q %v", w.Code, w.Body, w.Header())
	}
	if w := get("bytes=5-9"); w.Code != 206 || w.Body.String() != "56789" || w.Header().Get("Content-Range") != "bytes 5-9/20" {
		t.Errorf("range: %d %q %v", w.Code, w.Body, w.Header())
	}
	if w := get("bytes=-3"); w.Code != 206 || w.Body.String() != "hij" {
		t.Errorf("last bytes: %d %q", w.Code, w.Body)
	}
	for _, bad := range []string{"bytes=0-1,4-5", "bytes=-", "bytes=5-3", "bytes=-0", "items=0-1"} {
		if w := get(bad); w.Code != http.StatusRequestedRangeNotSatisfiable {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
	// Over: nothing more is read.
	close(done)
	if w := get(""); w.Body.Len() != 0 {
		t.Errorf("after the playback: %q", w.Body)
	}
	// Changed since: gone.
	later := time.Now().Add(time.Hour)
	os.Chtimes(path, later, later)
	if _, err := e.OpenFile(ctx, s); !errors.Is(err, ErrGone) {
		t.Errorf("changed: %v", err)
	}
	// Another file than the copy's, or one out of the libraries: refused.
	os.WriteFile(path, []byte("another file, longer"), 0o644)
	_, err = e.Start(ctx, p)
	refusedWith(t, err, 409, "file-changed")
	where = filepath.Join(lib, "link.mkv")
	_, err = e.Start(ctx, p)
	refusedWith(t, err, 409, "file-refused")
}
