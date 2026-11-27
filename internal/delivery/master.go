package delivery

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/msfp-io/msfp-go/wire"
)

// Overfin writes its own master playlist (Jellyfin's is unusable: a wrong
// BANDWIDTH, a fallback variant of its own, keys in its URIs): one
// variant, MediaPlaylist, with what MSFP media section 3.1 and playback
// section 2.7 ask of it: BANDWIDTH, CODECS, RESOLUTION, VIDEO-RANGE, and
// SUPPLEMENTAL-CODECS when Dolby Vision is kept in fragmented MP4.

// MediaPlaylist is the media playlist's neutral name in the master.
const MediaPlaylist = "v0.m3u8"

// Master is the master playlist of a delivery made of copy c, its audio
// track audio, as ordered (o) and made (m).
func Master(c wire.Copy, audio string, o Order, m *Made) []byte {
	var attrs []string
	codecs, supplemental := masterCodecs(c, audio, o, m)
	attrs = append(attrs, "BANDWIDTH="+strconv.FormatInt(bandwidth(c, audio, o, m), 10))
	if codecs != "" {
		attrs = append(attrs, "CODECS="+strconv.Quote(codecs))
	}
	if w, h := resolution(c, o, m); w > 0 && h > 0 {
		attrs = append(attrs, fmt.Sprintf("RESOLUTION=%dx%d", w, h))
	}
	if o.Video != nil {
		attrs = append(attrs, "VIDEO-RANGE="+videoRange(c, o, m))
	}
	if supplemental != "" {
		attrs = append(attrs, "SUPPLEMENTAL-CODECS="+strconv.Quote(supplemental))
	}
	version := 3
	if o.Segments == SegmentsMP4 {
		version = 7
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:%d\n#EXT-X-INDEPENDENT-SEGMENTS\n", version)
	b.WriteString("#EXT-X-STREAM-INF:" + strings.Join(attrs, ",") + "\n" + MediaPlaylist + "\n")
	return []byte(b.String())
}

// masterCodecs are the CODECS of what is served, and the Dolby Vision
// entry kept in fragmented MP4 (SUPPLEMENTAL-CODECS, with its
// compatibility brand), if any.
func masterCodecs(c wire.Copy, audio string, o Order, m *Made) (codecs, supplemental string) {
	var list []string
	if v := o.Video; v != nil {
		if v.Copy && c.Video != nil {
			var base string
			base, supplemental = wire.CopiedVideoCodecs(*c.Video, o.Segments == SegmentsMP4)
			if base != "" {
				list = append(list, base)
			}
		} else if !v.Copy {
			height := v.MaxHeight
			if m != nil && m.Height > 0 {
				height = m.Height
			}
			if s := madeVideoCodecs(v.Codec, height); s != "" {
				list = append(list, s)
			}
		}
	}
	if a := o.Audio; a != nil {
		s := ""
		if a.Copy {
			if t := track(c, audio); t != nil {
				s = t.Codecs
				if s == "" {
					s = madeAudioCodecs(t.Codec)
				}
			}
		} else {
			s = madeAudioCodecs(a.Codec)
		}
		if s != "" {
			list = append(list, s)
		}
	}
	return strings.Join(list, ","), supplemental
}

// madeVideoCodecs is a converted picture's codecs string, by its codec
// and height: what Jellyfin's encoders write (8-bit, High or Main
// profile), at the level the height calls for.
func madeVideoCodecs(codec string, height int) string {
	step := func(levels ...string) string {
		switch {
		case height <= 720:
			return levels[0]
		case height <= 1080:
			return levels[1]
		}
		return levels[2]
	}
	switch codec {
	case "h264":
		return step("avc1.64001f", "avc1.640028", "avc1.640033")
	case "hevc":
		return step("hvc1.1.6.L93.B0", "hvc1.1.6.L120.B0", "hvc1.1.6.L153.B0")
	case "av1":
		return step("av01.0.05M.08", "av01.0.08M.08", "av01.0.13M.08")
	}
	return ""
}

// madeAudioCodecs is the codecs string of audio, by its codec: the fixed
// ones (wire.FixedAudioCodecs), and for AAC, MP3 and DTS those Jellyfin's
// encoders write (LC, layer 3, the core), for a copied track whose own
// isn't known too.
func madeAudioCodecs(codec string) string {
	if s := wire.FixedAudioCodecs(codec); s != "" {
		return s
	}
	return map[string]string{"aac": "mp4a.40.2", "mp3": "mp4a.40.34", "dts": "dtsc"}[codec]
}

// bandwidth is the peak bits a second of what is served: the copy's own
// rate for a stream copied, the cap made of one converted, with the
// audio's share.
func bandwidth(c wire.Copy, audio string, o Order, m *Made) int64 {
	var total int64
	fileRate := c.BitsPerSecond()
	videoCopied := o.Video != nil && o.Video.Copy
	if videoCopied || o.Video == nil && o.Audio != nil && o.Audio.Copy {
		total = fileRate
	}
	if v := o.Video; v != nil && !v.Copy {
		rate := v.MaxBitrate
		if m != nil && m.VideoBitrate > 0 {
			rate = min(rate, m.VideoBitrate)
		}
		total = int64(rate)
	}
	if a := o.Audio; a != nil && !a.Copy {
		total += 192_000
		if a.Channels > 2 {
			total += 192_000
		}
	} else if a != nil && !videoCopied && o.Video != nil {
		total += 640_000 // a copied track beside a converted picture
	}
	if total <= 0 {
		total = 1_000_000
	}
	return total
}

// resolution is the picture served: the copy's when copied, else what
// was made (the height asked, the copy's shape).
func resolution(c wire.Copy, o Order, m *Made) (int, int) {
	v := o.Video
	switch {
	case v == nil:
		return 0, 0
	case v.Copy && c.Video != nil:
		return c.Video.Width, c.Video.Height
	case v.Copy:
		return 0, 0
	case m != nil && m.Width > 0 && m.Height > 0:
		return m.Width, m.Height
	}
	h := v.MaxHeight
	if c.Video == nil || c.Video.Height <= 0 || c.Video.Width <= 0 {
		return 0, 0
	}
	h = min(h, c.Video.Height)
	w := int(float64(h)*float64(c.Video.Width)/float64(c.Video.Height)/2+0.5) * 2
	return w, h
}

// videoRange is the served picture's VIDEO-RANGE: the copy's own when its
// range is kept (wire.KeptVideoRange, Dolby Vision kept only when
// copied), else SDR.
func videoRange(c wire.Copy, o Order, m *Made) string {
	v := o.Video
	kept := v.Copy || !v.ToneMap && (m == nil || !m.ToneMapped)
	if !kept || c.Video == nil {
		return "SDR"
	}
	return wire.KeptVideoRange(*c.Video, v.Copy)
}
