package msfp

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/overfin/server/msfp/wire"
)

// The data objects of the host interfaces (engine.md section 7).

// Library is one of the host's libraries: its own ID, its name and its
// kind ("movies", "tvshows", "music", "mixed"…, MSFP catalog section 2).
// Offered is set in the answer of Offers.
type Library struct {
	ID      string
	Name    string
	Kind    string
	Offered bool
}

// WantedLibrary is one of a peer's libraries: its public ID, its name
// and kind, whether this server wants it, and whether it was gone from
// the peer's latest answer.
type WantedLibrary struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Wanted bool   `json:"wanted,omitempty"`
	Gone   bool   `json:"gone,omitempty"`
}

// Title is a title of a content part: a *VideoTitle or a *MusicTitle.
type Title interface {
	// Common gives the members every title has.
	Common() *TitleCommon
}

// TitleCommon are the members of every title: its key, its kind, and the
// libraries that hold it (the host's IDs from Catalog; for a peer's
// title, the one public ID it was listed in).
type TitleCommon struct {
	Key       string   `json:"key"`
	Kind      string   `json:"kind"`
	Libraries []string `json:"libraries"`
}

// VideoTitle is a film, a series, a season or an episode (MSFP video
// section 3).
type VideoTitle struct {
	TitleCommon
	Audio     []string `json:"audio,omitempty"`
	Subtitles []string `json:"subtitles,omitempty"`
	// Pictures is the host's, for its own films and episodes: each copy's
	// picture, as its files say (any height, HDR formats among
	// "hdr10", "hlg" and "dv"); a copy whose size isn't known is left
	// out. The engine sends the best, stepped (MSFP video section 3.2).
	Pictures []Picture `json:"pictures,omitempty"`
	// Video is a peer's: the picture it sent of its best copy, cleaned
	// (MSFP video section 3.1); nil when it sent none.
	Video *Picture          `json:"video,omitempty"`
	With  []string          `json:"with,omitempty"`
	IDs   map[string]string `json:"ids,omitempty"`
}

// Picture is what a copy of a film or an episode offers for playing
// (MSFP video section 3.2): its height in pixels (for a picture wider
// than 16:9, a 16:9 picture's as wide: 2160 for 3840 × 1600), and its
// high dynamic range formats, "hdr10", "hlg" or "dv" (none: standard
// range).
type Picture struct {
	Height int      `json:"height"`
	HDR    []string `json:"hdr,omitempty"`
}

// Better says whether a copy of picture p is better than one of q, as
// BestPicture ranks them (both stepped).
func (p Picture) Better(q Picture) bool {
	return wire.Picture{Height: p.Height, HDR: p.HDR}.Better(wire.Picture{Height: q.Height, HDR: q.HDR})
}

// BestPicture is the picture of the best of a title's copies, as MSFP
// video section 3.2 says, stepped: the greatest height, then HDR before
// none, then more formats; false when no copy's size is known. A host
// lists its copies to Copies in that order, best first, so that play
// without a version plays it.
func BestPicture(pictures []Picture) (Picture, bool) {
	var best wire.Picture
	for _, p := range pictures {
		w := wire.Picture{Height: wire.StepHeight(p.Height), HDR: wire.CleanHDR(p.HDR)}
		if w.Height > 0 && (best.Height == 0 || w.Better(best)) {
			best = w
		}
	}
	return Picture{Height: best.Height, HDR: best.HDR}, best.Height > 0
}

// MusicTitle is an artist, an album or a track, with its description
// (MSFP music section 3): its name; for albums and tracks, the artist
// keys credited and their names as written, and the year; for tracks,
// the album key, the disc and number on it, and the length (0: unknown);
// other identifiers by scheme (mbrelease, mbtrack, isrc). A peer's are
// cleaned as music section 3.2 says; the host's own are checked the same
// way before they leave.
type MusicTitle struct {
	TitleCommon
	Name        string            `json:"name"`
	Artists     []string          `json:"artists,omitempty"`
	ArtistNames string            `json:"artistNames,omitempty"`
	Album       string            `json:"album,omitempty"`
	Disc        int               `json:"disc,omitempty"`
	Number      int               `json:"number,omitempty"`
	Year        int               `json:"year,omitempty"`
	DurationMs  int64             `json:"durationMs,omitempty"`
	IDs         map[string]string `json:"ids,omitempty"`
}

// Common gives the members every title has.
func (t *VideoTitle) Common() *TitleCommon { return &t.TitleCommon }

// Common gives the members every title has.
func (t *MusicTitle) Common() *TitleCommon { return &t.TitleCommon }

// titleJSON is how a stored title is written: one member naming its
// content part.
type titleJSON struct {
	Video *VideoTitle `json:"video,omitempty"`
	Music *MusicTitle `json:"music,omitempty"`
}

// MarshalTitle writes a title as JSON, for a store: {"video":{…}} or
// {"music":{…}}.
func MarshalTitle(t Title) ([]byte, error) {
	switch t := t.(type) {
	case *VideoTitle:
		return json.Marshal(titleJSON{Video: t})
	case *MusicTitle:
		return json.Marshal(titleJSON{Music: t})
	}
	return nil, errors.New("msfp: not a title")
}

// UnmarshalTitle reads what MarshalTitle wrote.
func UnmarshalTitle(b []byte) (Title, error) {
	var j titleJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, err
	}
	switch {
	case j.Video != nil:
		return j.Video, nil
	case j.Music != nil:
		return j.Music, nil
	}
	return nil, errors.New("msfp: not a title")
}

// TitlePage is a page of the host's titles: more says whether titles
// remain after the last.
type TitlePage struct {
	Titles []Title
	More   bool
}

// ChangePage is a page of what changed in the host's catalog: titles new
// or changed, keys gone, the page's last key, and whether more remain.
type ChangePage struct {
	Present []Title
	Gone    []string
	Last    string
	More    bool
}

// Copy is one of the host's copies of a title: its handle (opaque to the
// engine, never sent: the version ID a peer sees is a keyed MAC of it),
// its name (for versions), its audio languages as tags (for choosing the
// copy), and the host library holding it (one of those Copies was asked
// for: a playback ends when that library is no longer offered).
type Copy struct {
	Handle  string
	Name    string
	Audio   []string
	Library string
}

// Track is an audio or subtitle track of a copy: ID is its position,
// "a<n>" or "s<n>" (n from 0 to 999, no leading zeros). Label is its
// language's name in itself ("Français (Canada)"); Language a tag; Role
// "commentary" or "description" for audio beside the title's own sound;
// Pictures marks a PGS subtitle track (other picture formats are left
// out of the list).
type Track struct {
	ID              string `json:"id"`
	Label           string `json:"label"`
	Language        string `json:"language,omitempty"`
	Channels        int    `json:"channels,omitempty"`
	Role            string `json:"role,omitempty"`
	Default         bool   `json:"default,omitempty"`
	Forced          bool   `json:"forced,omitempty"`
	HearingImpaired bool   `json:"hearingImpaired,omitempty"`
	Pictures        bool   `json:"pictures,omitempty"`
}

// CopyInfo is what Playback.Describe tells of a copy.
type CopyInfo struct {
	AudioTracks    []Track
	SubtitleTracks []Track
	DurationMs     int64
	// EpisodeStartMs is where the episode played starts inside a copy
	// holding several (MSFP video section 5.2); 0 when unknown.
	EpisodeStartMs int64
	// Trickplay is the copy's previews, nil for none.
	Trickplay *Trickplay
}

// Trickplay is a copy's seek-bar previews (MSFP video section 5.5): the
// tiles' size, the grid of a sheet, the time between tiles, the tiles in
// all, and (in a RemotePlayback) the sheets listed.
type Trickplay struct {
	TileWidth, TileHeight int
	Columns, Rows         int
	IntervalMs            int
	TileCount             int
	Sheets                int
}

// Version is one of a title's copies as a peer sees it: an opaque ID and
// a name (MSFP playback section 2.3).
type Version struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// PlaybackInfo is how a playback is served, for the viewer's player
// information: why it is converted (none when played as it is), the
// container, the bitrate, the video and the audio.
type PlaybackInfo struct {
	Reasons     []string    `json:"reasons,omitempty"`
	Container   string      `json:"container,omitempty"`
	BitrateKbps int         `json:"bitrateKbps,omitempty"`
	Video       *StreamInfo `json:"video,omitempty"`
	Audio       *StreamInfo `json:"audio,omitempty"`
}

// StreamInfo describes the video or the audio of a playback.
type StreamInfo struct {
	Codec       string `json:"codec,omitempty"`
	Profile     string `json:"profile,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	BitDepth    int    `json:"bitDepth,omitempty"`
	Range       string `json:"range,omitempty"`
	BitrateKbps int    `json:"bitrateKbps,omitempty"`
	Channels    int    `json:"channels,omitempty"`
	Language    string `json:"language,omitempty"`
}

// Capabilities are what a viewer's device plays (MSFP playback section
// 2.1, video section 5.1). Toward a peer, only the values MSFP lists are
// sent, coarsened; toward the host, only those values, exactly as MSFP
// writes them. An empty list, or 0, says nothing.
type Capabilities struct {
	Containers       []string
	AudioCodecs      []string
	VideoCodecs      []string
	HDR              []string
	Subtitles        []string
	MaxAudioChannels int
	MaxHeight        int
	MaxBitrateKbps   int
}

// PeerRef names a peer for the host's live view: its ID and name, never
// a person.
type PeerRef struct {
	ID   string
	Name string
}

// StartRequest is what the engine asks Playback.Start to serve.
type StartRequest struct {
	// Copy is the handle of the copy chosen; Title the key played (an
	// episode's, for a copy holding several); AudioOnly, that it is a
	// track (MSFP music section 5): no video is served.
	Copy      string
	Title     string
	AudioOnly bool
	Peer      PeerRef
	// AudioTrack and SubtitleTrack are the engine's choice ("": none
	// shown); subtitles never make a playback transcode.
	AudioTrack    string
	SubtitleTrack string
	StartMs       int64
	// Capabilities are what the viewer's device plays, as the peer said,
	// only values MSFP lists. Nothing said about containers or video
	// codecs: no direct play (H.264 and AAC in HLS; AAC in HLS for a
	// track); about audio codecs: AAC only; about hdr: standard range.
	Capabilities Capabilities
	// MaxBitrateKbps is the cap in effect: 120,000 when none was set.
	MaxBitrateKbps int
	// Admit counts the pairing's streams (and, for "transcode", its
	// transcodes) in one step: the host calls it once it knows the
	// method, before starting any work (MSFP playback section 6.2).
	// Errors: ErrStreamLimitReached, ErrServerBusy.
	Admit func(method string) (Slot, error)
}

// Slot is a stream counted by Admit.
type Slot interface {
	// Release frees what was counted, when the host's start fails.
	Release()
	// Transcoding moves a remux that turned out to re-encode the video
	// into the transcode count; false when none is left.
	Transcoding() bool
}

// Started is what Playback.Start answers.
type Started struct {
	// Handle is the host's for this playback (not the copy's): Progress,
	// Stop and Media.Serve are given it.
	Handle   string
	Method   string // "direct", "remux", "transcode", as admitted
	Protocol string // "hls" or "file"
	// MimeType is the file's type, for "file" (a video/ or audio/ type).
	MimeType string
	// PositionMs is where it starts, in the copy.
	PositionMs int64
	Info       *PlaybackInfo
	// Segments are skip markers, when the host has them.
	Segments []Segment
}

// Segment is a skip marker: Kind is intro, outro, recap, preview or
// commercial (MSFP video section 5.3).
type Segment struct {
	Kind    string `json:"kind"`
	StartMs int64  `json:"startMs"`
	EndMs   int64  `json:"endMs"`
}

// EndReason is why a playback ended (MSFP playback section 6.3).
type EndReason string

// The reasons a playback ends.
const (
	EndStopped      EndReason = "stopped"
	EndExpired      EndReason = "expired"
	EndTooLong      EndReason = "tooLong"
	EndPairingEnded EndReason = "pairingEnded"
	EndNotOffered   EndReason = "notOffered"
	EndOperator     EndReason = "operator"
)

// MediaPath is what a media request asks for: Master, Playlist, Segment,
// File, Subtitles, Pictures or Sheet. Names are already checked against
// ^[A-Za-z0-9._-]{1,64}$, never "." or "..".
type MediaPath struct {
	Kind  MediaKind
	Name  string // Playlist, Segment
	Track string // Subtitles, Pictures
	Part  int    // Subtitles, Pictures
	Sheet int    // Sheet
}

// MediaKind is the kind of a MediaPath.
type MediaKind string

// The kinds of MediaPath.
const (
	MediaMaster    MediaKind = "master"
	MediaPlaylist  MediaKind = "playlist"
	MediaSegment   MediaKind = "segment"
	MediaFile      MediaKind = "file"
	MediaSubtitles MediaKind = "subtitles"
	MediaPictures  MediaKind = "pictures"
	MediaSheet     MediaKind = "sheet"
)

// Range is one byte range, parsed: First to Last included; Last -1 for
// "to the end"; Suffix for "the last Suffix bytes" (First and Last
// unused).
type Range struct {
	First, Last, Suffix int64
}

// Limits are what a pairing may use of a server at once (MSFP playback
// section 5): streams (0 to 50), transcodes among them, a bitrate cap
// (0: none). With Audio, audio playbacks (tracks) are counted apart,
// within AudioStreams and AudioTranscodes (music section 5.1); without,
// they count in Streams and Transcodes.
type Limits struct {
	Streams         int  `json:"streams"`
	Transcodes      int  `json:"transcodes"`
	BitrateKbps     int  `json:"bitrateKbps,omitempty"`
	Audio           bool `json:"audio,omitempty"`
	AudioStreams    int  `json:"audioStreams,omitempty"`
	AudioTranscodes int  `json:"audioTranscodes,omitempty"`
}

// Check returns ErrInvalidLimits when l is out of range.
func (l Limits) Check() error {
	if l.Streams < 0 || l.Streams > 50 || l.Transcodes < 0 || l.Transcodes > l.Streams ||
		l.BitrateKbps < 0 || l.BitrateKbps > 1_000_000 ||
		l.AudioStreams < 0 || l.AudioStreams > 50 || l.AudioTranscodes < 0 || l.AudioTranscodes > l.AudioStreams {
		return ErrInvalidLimits
	}
	return nil
}

// ownChangeJSON is how a stored OwnChange is written.
type ownChangeJSON struct {
	Seq   int64           `json:"seq"`
	Key   string          `json:"key"`
	Title json.RawMessage `json:"title,omitempty"`
	At    time.Time       `json:"at"`
}

// MarshalJSON writes the change, its title as MarshalTitle does.
func (c OwnChange) MarshalJSON() ([]byte, error) {
	j := ownChangeJSON{Seq: c.Seq, Key: c.Key, At: c.At}
	if c.Title != nil {
		b, err := MarshalTitle(c.Title)
		if err != nil {
			return nil, err
		}
		j.Title = b
	}
	return json.Marshal(j)
}

// UnmarshalJSON reads what MarshalJSON wrote.
func (c *OwnChange) UnmarshalJSON(b []byte) error {
	var j ownChangeJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	*c = OwnChange{Seq: j.Seq, Key: j.Key, At: j.At}
	if len(j.Title) > 0 {
		t, err := UnmarshalTitle(j.Title)
		if err != nil {
			return err
		}
		c.Title = t
	}
	return nil
}
