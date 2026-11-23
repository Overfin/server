package delivery_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/msfp-io/msfp-go/describe"
	"github.com/msfp-io/msfp-go/wire"
	"github.com/overfin/server/internal/copies"
	"github.com/overfin/server/internal/delivery"
	"github.com/overfin/server/internal/files"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/store"
)

// A live test of the executor with the companion plugin's deliveries, on
// a test Jellyfin (never a real one) with the plugin installed and
// Overfin's key set in it. It runs only when told where:
//
//	OVERFIN_LIVE_JELLYFIN  the test Jellyfin's address
//	OVERFIN_LIVE_KEY       the API key set in the plugin (Overfin's)
//	OVERFIN_LIVE_TOKEN     a user's token, for the segments (as players' are fetched)
//	OVERFIN_LIVE_MAP       "<Jellyfin's folder>=<this machine's>", its media's
//	OVERFIN_LIVE_DOCKER    the test Jellyfin's container, to count its ffmpegs and files
//	OVERFIN_LIVE_WATCHDOG  1: also wait for the plugin's watchdog (over two minutes)
//
// It needs ffprobe on this machine.

type live struct {
	t        *testing.T
	jf       *jellyfin.Client
	admin    jellyfin.Auth
	user     jellyfin.Auth
	from, to string
	docker   string
	out      string
}

func (l *live) hostPath(p string) string {
	if strings.HasPrefix(p, l.from+"/") {
		return l.to + strings.TrimPrefix(p, l.from)
	}
	return p
}

// docker runs a command in the test Jellyfin's container.
func (l *live) dockerOut(args ...string) string {
	if l.docker == "" {
		return ""
	}
	out, _ := exec.Command("docker", append([]string{"exec", l.docker}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out))
}

func (l *live) ffmpegs() int {
	out := l.dockerOut("sh", "-c", "for p in /proc/[0-9]*; do cat $p/comm 2>/dev/null; done | grep -c '^ffmpeg$'")
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return n
}

func (l *live) transcodeFiles() string {
	return l.dockerOut("sh", "-c", "ls /cache/transcodes 2>/dev/null | wc -l")
}

// copyOf is a version as the copy index would keep it: read with
// describe from this machine's copy of the file.
func (l *live) copyOf(f jellyfin.VideoFile) store.IndexedCopy {
	l.t.Helper()
	path := l.hostPath(f.Path)
	pr, err := (&describe.Prober{Frames: true}).Probe(context.Background(), path)
	if err != nil {
		l.t.Fatalf("probe %s: %v", filepath.Base(path), err)
	}
	c, streams, _ := copies.Shape(pr, f)
	dir := filepath.Dir(path)
	libs := files.NewLibraries([]string{dir})
	file, err := libs.Open(path)
	if err != nil {
		l.t.Fatal(err)
	}
	fp, _ := files.Fingerprint(file)
	size := file.Info.Size()
	file.Close()
	return store.IndexedCopy{CopyID: "v" + strings.Repeat("a", 26), ItemID: f.ItemID, SourceID: f.SourceID, Fingerprint: fp,
		ETag: "etagetagetagetagetaget", Size: size, Facts: c, Streams: streams}
}

func (l *live) executor(m delivery.Maker) *delivery.Executor {
	return &delivery.Executor{Maker: m,
		Path: func(ctx context.Context, item, source string) (string, error) {
			var found string
			err := l.jf.VideoFiles(ctx, l.admin, func(f jellyfin.VideoFile) bool {
				if f.ItemID == item && f.SourceID == source {
					found = l.hostPath(f.Path)
					return false
				}
				return true
			})
			return found, err
		},
		Libraries: func(context.Context) (files.Libraries, error) { return files.NewLibraries([]string{l.to}), nil },
		Fingerprint: func(_ context.Context, _, _ string, f *files.File) (string, error) {
			return files.Fingerprint(f)
		}}
}

// fetch gets the segments of a started delivery as a player would,
// through Overfin's rewritten media playlist: the init segment (fMP4),
// then each segment named, and probes them.
func (l *live) fetch(name string, c store.IndexedCopy, s *delivery.Started, segments ...int) []map[string]any {
	l.t.Helper()
	ctx := context.Background()
	rw, err := playback.RewriteVariant(s.Made.Playlist, playback.Upstream{Path: "/Videos/" + c.ItemID + "/main.m3u8"})
	if err != nil {
		l.t.Fatalf("%s: rewriting the media playlist: %v\n%s", name, err, s.Made.Playlist)
	}
	if strings.Contains(string(rw.Body), "ApiKey") || strings.Contains(string(rw.Body), "api_key") {
		l.t.Errorf("%s: a key in the playlist", name)
	}
	ext := ".ts"
	if s.Made.Segments == delivery.SegmentsMP4 {
		ext = ".mp4"
	}
	dir := filepath.Join(l.out, name)
	os.MkdirAll(dir, 0o755)
	get := func(neutral string) string {
		up, ok := rw.Files[neutral]
		if !ok {
			l.t.Fatalf("%s: no %s in %v", name, neutral, len(rw.Files))
		}
		res, err := l.jf.Open(ctx, l.user, up.Path, up.Query, "")
		if err != nil {
			l.t.Fatalf("%s: %s: %v", name, neutral, err)
		}
		defer res.Body.Close()
		path := filepath.Join(dir, neutral)
		f, _ := os.Create(path)
		io.Copy(f, res.Body)
		f.Close()
		return path
	}
	var init string
	if ext == ".mp4" {
		init = get("init.mp4")
	}
	var probes []map[string]any
	for _, n := range segments {
		seg := get("seg" + strconv.Itoa(n) + ext)
		if init != "" {
			whole := seg + ".whole.mp4"
			a, _ := os.ReadFile(init)
			b, _ := os.ReadFile(seg)
			os.WriteFile(whole, append(a, b...), 0o644)
			seg = whole
		}
		out, err := exec.Command("ffprobe", "-v", "error", "-show_entries",
			"stream=codec_type,codec_name,codec_tag_string,profile,width,height,pix_fmt,color_transfer,channels:stream_side_data=side_data_type,dv_profile:format=start_time",
			"-of", "json", seg).Output()
		if err != nil {
			l.t.Fatalf("%s: ffprobe seg%d: %v", name, n, err)
		}
		var p map[string]any
		json.Unmarshal(out, &p)
		probes = append(probes, p)
	}
	return probes
}

// streamOf is the first probed stream of a kind.
func streamOf(p map[string]any, kind string) map[string]any {
	list, _ := p["streams"].([]any)
	for _, e := range list {
		if s, ok := e.(map[string]any); ok && s["codec_type"] == kind {
			return s
		}
	}
	return nil
}

func TestLive(t *testing.T) {
	base, key := os.Getenv("OVERFIN_LIVE_JELLYFIN"), os.Getenv("OVERFIN_LIVE_KEY")
	if base == "" || key == "" {
		t.Skip("no test Jellyfin (OVERFIN_LIVE_JELLYFIN, OVERFIN_LIVE_KEY)")
	}
	jf, err := jellyfin.New(base, "live-test")
	if err != nil {
		t.Fatal(err)
	}
	from, to, _ := strings.Cut(os.Getenv("OVERFIN_LIVE_MAP"), "=")
	l := &live{t: t, jf: jf, admin: jellyfin.Auth{Token: key, DeviceID: "overfin-live-admin"},
		user: jellyfin.Auth{Token: os.Getenv("OVERFIN_LIVE_TOKEN"), DeviceID: "overfin-play-1"},
		from: from, to: to, docker: os.Getenv("OVERFIN_LIVE_DOCKER"), out: t.TempDir()}
	ctx := context.Background()
	maker := &delivery.PluginMaker{Jellyfin: jf, Admin: func() jellyfin.Auth { return l.admin }}
	e := l.executor(maker)
	if err := e.Clear(ctx); err != nil {
		t.Fatalf("clear: %v", err)
	}
	enc, err := maker.Encoders(ctx)
	if err != nil {
		t.Fatalf("encoders: %v", err)
	}
	t.Logf("encoders: %+v", enc)

	byName := map[string]jellyfin.VideoFile{}
	jf.VideoFiles(ctx, l.admin, func(f jellyfin.VideoFile) bool {
		byName[filepath.Base(f.Path)] = f
		return true
	})
	file := func(name string) (jellyfin.VideoFile, bool) {
		f, ok := byName[name]
		if !ok {
			t.Logf("no %s in the test Jellyfin: its cases skipped", name)
		}
		return f, ok
	}
	start := func(name string, c store.IndexedCopy, r delivery.Request) *delivery.Started {
		t.Helper()
		p, err := e.Plan(ctx, c, r)
		if err != nil {
			t.Fatalf("%s: plan: %v", name, err)
		}
		began := time.Now()
		s, err := e.Start(ctx, p)
		if err != nil {
			t.Fatalf("%s: start: %v", name, err)
		}
		t.Logf("%s: %s, made %+v in %v; ffmpegs %d", name, p.Kind, *withoutPlaylist(s.Made), time.Since(began).Round(time.Millisecond), l.ffmpegs())
		t.Logf("%s: master\n%s", name, s.Master)
		return s
	}
	stop := func(name string, s *delivery.Started) {
		t.Helper()
		if err := e.Stop(ctx, s); err != nil {
			t.Errorf("%s: stop: %v", name, err)
		}
		time.Sleep(3 * time.Second)
		if n := l.ffmpegs(); n != 0 {
			t.Errorf("%s: %d ffmpeg left after the stop", name, n)
		}
		if n := l.transcodeFiles(); n != "" && n != "0" {
			t.Errorf("%s: %s transcode files left after the stop", name, n)
		}
	}
	refusedAs := func(name string, c store.IndexedCopy, r delivery.Request, why string) {
		t.Helper()
		p, err := e.Plan(ctx, c, r)
		if err == nil {
			_, err = e.Start(ctx, p)
		}
		rf := delivery.AsRefusal(err)
		if rf == nil || !strings.Contains(rf.Why, why) {
			t.Errorf("%s: %v, want a refusal %s", name, err, why)
			return
		}
		t.Logf("%s: refused %d %s", name, rf.Status, rf.Why)
		if n := l.ffmpegs(); n != 0 {
			time.Sleep(3 * time.Second)
			if n = l.ffmpegs(); n != 0 {
				t.Errorf("%s: %d ffmpeg left after a refusal", name, n)
			}
		}
	}

	if f, ok := file("a-h264-sdr.mkv"); ok {
		c := l.copyOf(f)
		// A remux into fMP4, the second audio track.
		second := c.Facts.AudioTracks[len(c.Facts.AudioTracks)-1].ID
		s := start("remux", c, delivery.Request{Play: wire.PlayRequest{Delivery: wire.DeliveryRemux, MediaType: "video/mp4", AudioTrack: second}})
		p := l.fetch("remux", c, s, 0, 1)
		if v := streamOf(p[0], "video"); v == nil || v["codec_name"] != "h264" || v["height"] != 1080.0 {
			t.Errorf("remux: video %v", v)
		}
		if a := streamOf(p[0], "audio"); a == nil || a["codec_name"] != c.Facts.AudioTracks[len(c.Facts.AudioTracks)-1].Codec {
			t.Errorf("remux: audio %v", a)
		}
		if n := l.ffmpegs(); n > 1 {
			t.Errorf("remux: %d ffmpeg for one delivery", n)
		}
		stop("remux", s)

		// The baseline, from 40 s in: ffmpeg starts at that segment; a seek
		// back restarts it as ordered.
		s = start("baseline", c, delivery.Request{Play: wire.PlayRequest{StartPositionMs: 40_000}})
		p = l.fetch("baseline", c, s, 6, 7, 1)
		for i, pr := range p {
			if a := streamOf(pr, "audio"); a == nil || a["codec_name"] != "aac" || a["channels"] != 2.0 {
				t.Errorf("baseline seg %d: audio %v", i, a)
			}
		}
		stop("baseline", s)

		// A transcode: H.264 720p 3 Mbps, AAC stereo, in MPEG-TS.
		s = start("h264-720", c, delivery.Request{Play: wire.PlayRequest{Delivery: wire.DeliveryTranscode,
			Transcode: &wire.TranscodeTargets{VideoCodec: "h264", MaxHeight: 720, MaxBitrateKbps: 3000}}})
		p = l.fetch("h264-720", c, s, 0, 8)
		for i, pr := range p {
			if v := streamOf(pr, "video"); v == nil || v["height"] != 720.0 || v["codec_name"] != "h264" {
				t.Errorf("h264-720 seg %d: video %v", i, v)
			}
		}
		stop("h264-720", s)

		// HEVC in fMP4: hvc1.
		s = start("hevc-720", c, delivery.Request{Play: wire.PlayRequest{Delivery: wire.DeliveryTranscode, MediaType: "video/mp4",
			Transcode: &wire.TranscodeTargets{VideoCodec: "hevc", MaxHeight: 720, MaxBitrateKbps: 3000}}})
		p = l.fetch("hevc-720", c, s, 0)
		if v := streamOf(p[0], "video"); v == nil || v["codec_tag_string"] != "hvc1" || v["height"] != 720.0 {
			t.Errorf("hevc-720: video %v", v)
		}
		stop("hevc-720", s)

		// Jellyfin's sizes by bitrate: 1080p at 1 Mbps is made 540p, refused.
		refusedAs("1080-at-1M", c, delivery.Request{Play: wire.PlayRequest{Delivery: wire.DeliveryTranscode,
			Transcode: &wire.TranscodeTargets{MaxHeight: 1080, MaxBitrateKbps: 1000}}}, "height-cap")

		// The file itself, from disk.
		pf, err := e.Plan(ctx, c, delivery.Request{Own: true, Play: wire.PlayRequest{Delivery: wire.DeliveryFile}})
		if err != nil {
			t.Fatal(err)
		}
		sf, err := e.Start(ctx, pf)
		if err != nil {
			t.Fatalf("file: %v", err)
		}
		ff, err := e.OpenFile(ctx, sf)
		if err != nil || ff.Info.Size() != c.Size {
			t.Errorf("file: %v", err)
		} else {
			ff.Close()
		}
		if l.ffmpegs() != 0 {
			t.Error("file: an ffmpeg runs")
		}

		if os.Getenv("OVERFIN_LIVE_WATCHDOG") == "1" {
			s = start("watchdog", c, delivery.Request{})
			l.fetch("watchdog", c, s, 0)
			t.Log("watchdog: waiting 135 s without a ping or a read")
			time.Sleep(135 * time.Second)
			if err := e.Ping(ctx, s); !errors.Is(err, delivery.ErrGone) {
				t.Errorf("watchdog: ping after 135 s: %v", err)
			}
			if n := l.ffmpegs(); n != 0 {
				t.Errorf("watchdog: %d ffmpeg left", n)
			}
			if n := l.transcodeFiles(); n != "" && n != "0" {
				t.Errorf("watchdog: %s transcode files left", n)
			}
			// Pings keep a paused player's delivery past Jellyfin's own 60 s.
			s = start("pinged", c, delivery.Request{})
			l.fetch("pinged", c, s, 0)
			for range 3 {
				time.Sleep(30 * time.Second)
				if err := e.Ping(ctx, s); err != nil {
					t.Errorf("pinged: %v", err)
				}
			}
			stop("pinged", s)
		}
	}

	if f, ok := file("b-hevc-hdr10.mkv"); ok {
		c := l.copyOf(f)
		// HDR10 remuxed in fMP4: PQ kept, hvc1.
		s := start("hdr-remux", c, delivery.Request{Play: wire.PlayRequest{Delivery: wire.DeliveryRemux, MediaType: "video/mp4"}})
		p := l.fetch("hdr-remux", c, s, 0)
		if v := streamOf(p[0], "video"); v == nil || v["color_transfer"] != "smpte2084" || v["codec_tag_string"] != "hvc1" {
			t.Errorf("hdr-remux: video %v", v)
		}
		if !strings.Contains(string(s.Master), "VIDEO-RANGE=PQ") {
			t.Errorf("hdr-remux: master %s", s.Master)
		}
		stop("hdr-remux", s)
		// Tone mapped to H.264 SDR.
		s = start("tonemap", c, delivery.Request{Play: wire.PlayRequest{Delivery: wire.DeliveryTranscode,
			Transcode: &wire.TranscodeTargets{MaxHeight: 720, MaxBitrateKbps: 4000}}})
		p = l.fetch("tonemap", c, s, 0)
		if v := streamOf(p[0], "video"); v == nil || v["color_transfer"] == "smpte2084" || v["pix_fmt"] != "yuv420p" {
			t.Errorf("tonemap: video %v", v)
		}
		stop("tonemap", s)
		// HDR kept while converting: not on a CPU.
		refusedAs("hdr-keep", c, delivery.Request{Play: wire.PlayRequest{Delivery: wire.DeliveryTranscode,
			Transcode: &wire.TranscodeTargets{VideoCodec: "hevc", Range: wire.RangeKeep}}}, "range")
	}

	if f, ok := file("dv84-loop.mkv"); ok {
		c := l.copyOf(f)
		s := start("dv-remux", c, delivery.Request{Play: wire.PlayRequest{Delivery: wire.DeliveryRemux, MediaType: "video/mp4"}})
		p := l.fetch("dv-remux", c, s, 0)
		v := streamOf(p[0], "video")
		if v == nil || v["codec_tag_string"] != "hvc1" {
			t.Errorf("dv-remux: video %v", v)
		}
		if !strings.Contains(string(s.Master), "SUPPLEMENTAL-CODECS=\"dvh1.08") {
			t.Errorf("dv-remux: master %s", s.Master)
		}
		stop("dv-remux", s)
	}
	if err := e.Clear(ctx); err != nil {
		t.Errorf("clear: %v", err)
	}
}

func withoutPlaylist(m *delivery.Made) *delivery.Made {
	c := *m
	c.Playlist = nil
	return &c
}
