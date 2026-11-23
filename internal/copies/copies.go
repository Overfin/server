// Package copies keeps the copy index (catalog_copies): every version of
// a film or an episode as an MSFP copy (video sections 3.2 to 3.6), read
// by Overfin itself from the file's headers with msfp-go's describe, or
// built from Jellyfin's data until it is (a minimal copy, played as the
// baseline only).
//
// What is Overfin's own lives here: which files may be read (package
// files, rule 78), the copy's ID and etag (keyed hashes of the file's
// path in its library and of its fingerprint), the subtitle files beside
// a video as Jellyfin lists them, the map from MSFP's track IDs to
// Jellyfin's stream indexes, and the raw probes, kept by fingerprint
// without paths, for the identifier.
package copies

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/msfp-io/msfp-go/describe"
	"github.com/msfp-io/msfp-go/wire"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tracks"
)

// Version is the probe's and the shaping's version: a copy read with an
// older one is read again (probed, its raw probe kept again). Raise it
// when the probe or the shaping changes what a copy says.
const Version = 1

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func mac(key []byte, parts ...string) []byte {
	m := hmac.New(sha256.New, key)
	for _, p := range parts {
		m.Write([]byte(strconv.Itoa(len(p)) + ":" + p))
	}
	return m.Sum(nil)
}

// CopyID is the ID of the copy a file is: "v" and 26 base32 characters
// of a keyed hash (keys.CopyID) of its library folder (root) and its path
// in it (rel), so two libraries holding the same path (Films and Films
// 4K) give two copies. It doesn't depend on Jellyfin's IDs; a rename or a
// moved library folder gives another.
func CopyID(key []byte, root, rel string) string {
	return "v" + strings.ToLower(idEncoding.EncodeToString(mac(key, "file", root, rel)[:16]))
}

// EpisodeCopyID is the ID of the copy of an episode a file holds after
// its first (a file of S01E01-E02 is one item; its copy, fileCopyID): a
// keyed hash of the file's copy ID and the episode's title key, so each
// episode's copy has its own ID, no path kept.
func EpisodeCopyID(key []byte, fileCopyID, titleKey string) string {
	return "v" + strings.ToLower(idEncoding.EncodeToString(mac(key, "episode", fileCopyID, titleKey)[:16]))
}

// ETag is the etag of the copy a file is: the first 22 characters of the
// base64url keyed hash (keys.Etag) of its fingerprint. It changes when
// the file does, and tells nothing across servers.
func ETag(key []byte, fingerprint string) string {
	return base64.RawURLEncoding.EncodeToString(mac(key, fingerprint))[:22]
}

// externals are the subtitle files beside a version, in Jellyfin's
// order, and a hash of them: when it changes, the copy is shaped again.
func externals(f jellyfin.VideoFile) ([]jellyfin.MediaStream, string) {
	var out []jellyfin.MediaStream
	for _, s := range f.Streams {
		if s.IsExternal && s.Type == "Subtitle" {
			out = append(out, s)
		}
	}
	slices.SortStableFunc(out, func(a, b jellyfin.MediaStream) int { return a.Index - b.Index })
	if len(out) == 0 {
		return nil, ""
	}
	h := sha256.New()
	for _, s := range out {
		h.Write([]byte(strings.Join([]string{strconv.Itoa(s.Index), s.Codec, s.Language, s.Title,
			strconv.FormatBool(s.IsDefault), strconv.FormatBool(s.IsForced), strconv.FormatBool(s.IsHearingImpaired)}, "\x00") + "\n"))
	}
	return out, hex.EncodeToString(h.Sum(nil))[:32]
}

// subtitleFormat is a Jellyfin subtitle codec as the registry's token.
func subtitleFormat(codec string) string {
	c := strings.ToLower(codec)
	switch c {
	case "srt", "text":
		c = "subrip"
	case "vtt":
		c = "webvtt"
	case "pgssub", "pgs":
		c = "hdmv_pgs_subtitle"
	case "dvdsub":
		c = "dvd_subtitle"
	}
	if !wire.Token(c) {
		return ""
	}
	return c
}

// describeExternals are the subtitle files as describe numbers them.
func describeExternals(ext []jellyfin.MediaStream) []describe.External {
	var out []describe.External
	for _, s := range ext {
		out = append(out, describe.External{Language: s.Language, Format: subtitleFormat(s.Codec), Default: s.IsDefault,
			Forced: s.IsForced, HearingImpaired: s.IsHearingImpaired})
	}
	return out
}

// embedded are a version's streams in its file, as Jellyfin lists them,
// by type (Video, Audio, Subtitle), each in Jellyfin's order.
func embedded(f jellyfin.VideoFile) map[string][]jellyfin.MediaStream {
	out := map[string][]jellyfin.MediaStream{}
	for _, s := range f.Streams {
		if !s.IsExternal {
			out[s.Type] = append(out[s.Type], s)
		}
	}
	for _, l := range out {
		slices.SortStableFunc(l, func(a, b jellyfin.MediaStream) int { return a.Index - b.Index })
	}
	return out
}

// Shape is the copy a probe describes (describe.Shape, with the subtitle
// files Jellyfin lists beside it), the map of its track IDs to
// Jellyfin's stream indexes, and its languages as the catalog keeps them.
// The copy's ID, etag, previews and segments are left to the caller.
//
// Jellyfin numbers streams its own way (the subtitle files among them),
// so the map pairs them by kind, in order: the n-th audio stream of the
// file is Jellyfin's n-th audio stream, and so on; a kind whose count
// differs isn't mapped (that track can't be asked of Jellyfin). The
// files beside it are paired in Jellyfin's order.
func Shape(pr *describe.Probe, f jellyfin.VideoFile) (wire.Copy, map[string]int, *store.CopyLanguages) {
	ext, _ := externals(f)
	c := describe.Shape(pr, describeExternals(ext))
	c.Parts = 0
	if f.Parts > 1 {
		c.Parts = min(f.Parts, wire.MaxCopyParts)
	}
	streams := map[string]int{}
	jf := embedded(f)
	byKind := map[string][]describe.Stream{}
	for _, s := range pr.Streams {
		switch {
		case s.CodecType == "video" && s.Disposition.AttachedPic == 0 && s.Disposition.StillImage == 0:
			byKind["Video"] = append(byKind["Video"], s)
		case s.CodecType == "audio":
			byKind["Audio"] = append(byKind["Audio"], s)
		case s.CodecType == "subtitle":
			byKind["Subtitle"] = append(byKind["Subtitle"], s)
		}
	}
	ids := map[string]bool{}
	for _, a := range c.AudioTracks {
		ids[a.ID] = true
	}
	for _, s := range c.SubtitleTracks {
		ids[s.ID] = true
	}
	for kind, file := range byKind {
		if len(file) != len(jf[kind]) {
			continue
		}
		for i, s := range file {
			id := ""
			switch kind {
			case "Video":
				if i == 0 && c.Video != nil {
					id = "v"
				}
			case "Audio":
				id = "a" + strconv.Itoa(s.Index)
			case "Subtitle":
				id = "s" + strconv.Itoa(s.Index)
			}
			if id == "v" || ids[id] {
				streams[id] = jf[kind][i].Index
			}
		}
	}
	n := 0
	for _, t := range c.SubtitleTracks {
		if t.External && n < len(ext) {
			streams[t.ID] = ext[n].Index
			n++
		}
	}
	// Its languages, by the rules the player lists tracks with (a region
	// from the track's title, "VFQ"), from the file's own tags.
	var ms []jellyfin.MediaStream
	for _, s := range pr.Streams {
		m := jellyfin.MediaStream{Index: s.Index, Codec: s.CodecName, Language: s.Tags.Get("language"), Title: s.Tags.Get("title"),
			Channels: s.Channels, IsDefault: s.Disposition.Default == 1, IsForced: s.Disposition.Forced == 1,
			IsHearingImpaired: s.Disposition.HearingImpaired == 1}
		switch s.CodecType {
		case "audio":
			m.Type = "Audio"
		case "subtitle":
			m.Type, m.IsTextSubtitleStream = "Subtitle", textSubtitle(s.CodecName)
		default:
			continue
		}
		ms = append(ms, m)
	}
	ms = append(ms, ext...)
	var l store.CopyLanguages
	l.AudioLanguages, l.Audio, l.Subtitles = tracks.Languages(ms)
	return c, streams, &l
}

// textSubtitle says whether an ffprobe subtitle codec is text.
func textSubtitle(codec string) bool {
	switch codec {
	case "subrip", "srt", "ass", "ssa", "webvtt", "mov_text", "text", "microdvd", "subviewer", "subviewer1", "sami",
		"realtext", "stl", "jacosub", "mpl2", "pjs", "vplayer":
		return true
	}
	return false
}

// Minimal is the copy Jellyfin's data describes, for a file not probed
// yet or whose probe failed (video section 3.2): its size, duration,
// container as a media type, picture size and codec, and audio tracks
// with their languages, without codecs strings, so it is never played as
// it is (the baseline plays it). Its track IDs are file indexes as
// Jellyfin's suggest them. Its size is Jellyfin's; the caller sets the
// file's own when it has it.
func Minimal(f jellyfin.VideoFile) (wire.Copy, map[string]int) {
	c := wire.Copy{Size: max(f.Size, 0), AudioTracks: []wire.CopyAudio{}}
	if ms := f.RunTimeTicks / 10_000; ms > 0 && ms <= wire.MaxCopyDurationMs {
		c.DurationMs = ms
	}
	if f.Parts > 1 {
		c.Parts = min(f.Parts, wire.MaxCopyParts)
	}
	jf := embedded(f)
	ext, _ := externals(f)
	// Jellyfin numbers the files beside a video first, then the file's
	// own streams after them; else from 0.
	offset := 0
	if len(ext) > 0 && len(f.Streams) > len(ext) {
		lowest := -1
		for _, l := range jf {
			for _, s := range l {
				if lowest < 0 || s.Index < lowest {
					lowest = s.Index
				}
			}
		}
		if lowest >= len(ext) && ext[len(ext)-1].Index < lowest {
			offset = len(ext)
		}
	}
	streams := map[string]int{}
	picture := false
	if v := jf["Video"]; len(v) > 0 && v[0].Width > 0 && v[0].Height > 0 && v[0].Width <= wire.MaxVideoSide && v[0].Height <= wire.MaxVideoSide {
		picture = true
		if codec := strings.ToLower(v[0].Codec); wire.Token(codec) {
			c.Video = &wire.VideoStream{Codec: codec, Width: v[0].Width, Height: v[0].Height}
			streams["v"] = v[0].Index
		}
	}
	// Jellyfin hasn't read its streams yet: a film's or an episode's file
	// is a video.
	if len(f.Streams) == 0 && !f.NoVideo {
		picture = true
	}
	c.MediaType = describe.AudioOrVideo(describe.ContainerType(f.Container), picture)
	for _, s := range jf["Audio"] {
		codec := strings.ToLower(s.Codec)
		if codec == "dca" {
			codec = "dts"
		}
		if !wire.Token(codec) || s.Index-offset < 0 || len(c.AudioTracks) == wire.MaxAudioTracks {
			continue
		}
		a := wire.CopyAudio{ID: "a" + strconv.Itoa(s.Index-offset), Codec: codec, Default: s.IsDefault, Forced: s.IsForced}
		a.Language, _ = wire.SentTag(s.Language)
		if s.Channels >= 1 && s.Channels <= 64 {
			a.Channels = s.Channels
		}
		c.AudioTracks = append(c.AudioTracks, a)
		streams[a.ID] = s.Index
	}
	return c, streams
}

// ---- Raw probes ----------------------------------------------------------------

// raw is a probe as kept (store.CopyProbe.Raw): describe's outputs, the
// first run's without the file's name, its attachments, or any string
// holding the file's library folder.
type raw struct {
	MediaType string           `json:"mediaType"`
	Size      int64            `json:"size"`
	Outputs   describe.Outputs `json:"outputs"`
}

// keepable is a probe's outputs as kept: format.filename removed (it is
// the descriptor ffprobe read), attachment streams removed, and every
// string holding dir (the file's library folder) removed.
func keepable(pr *describe.Probe, dir string) ([]byte, error) {
	var tree map[string]any
	d := json.NewDecoder(strings.NewReader(string(pr.Outputs.Streams)))
	d.UseNumber()
	if err := d.Decode(&tree); err != nil {
		return nil, err
	}
	if f, ok := tree["format"].(map[string]any); ok {
		delete(f, "filename")
	}
	if list, ok := tree["streams"].([]any); ok {
		kept := []any{}
		for _, e := range list {
			if s, ok := e.(map[string]any); ok && s["codec_type"] != "attachment" {
				kept = append(kept, s)
			}
		}
		tree["streams"] = kept
	}
	if dir = filepath.Clean(dir); dir != "." && dir != string(filepath.Separator) {
		scrub(tree, dir)
	}
	first, err := json.Marshal(tree)
	if err != nil {
		return nil, err
	}
	out := pr.Outputs
	out.Streams = first
	return json.Marshal(raw{MediaType: pr.MediaType, Size: pr.Size, Outputs: out})
}

// scrub removes from a JSON tree every string holding dir.
func scrub(v any, dir string) {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			if s, ok := e.(string); ok && strings.Contains(s, dir) {
				delete(t, k)
			} else {
				scrub(e, dir)
			}
		}
	case []any:
		for i, e := range t {
			if s, ok := e.(string); ok && strings.Contains(s, dir) {
				t[i] = ""
			} else {
				scrub(e, dir)
			}
		}
	}
}

// parseKept reads a kept probe again.
func parseKept(b []byte) (*describe.Probe, error) {
	var r raw
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	if r.MediaType == "" || r.Size < 0 {
		return nil, errors.New("copies: not a kept probe")
	}
	return describe.Parse(r.MediaType, r.Size, r.Outputs)
}
