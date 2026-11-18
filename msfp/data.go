package msfp

import (
	"encoding/json"
	"errors"
	"time"
)

// The data objects of the host interfaces (engine.md section 7). Those of
// the catalog, playback and media are defined now and used from later
// steps of the engine on.

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
	Audio     []string          `json:"audio,omitempty"`
	Subtitles []string          `json:"subtitles,omitempty"`
	With      []string          `json:"with,omitempty"`
	IDs       map[string]string `json:"ids,omitempty"`
}

// MusicTitle is a track (MSFP music section 3).
type MusicTitle struct {
	TitleCommon
	Name        string            `json:"name"`
	Artists     []string          `json:"artists,omitempty"`
	ArtistNames []string          `json:"artistNames,omitempty"`
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
// engine, never sent), its name (for versions), its audio languages.
type Copy struct {
	Handle string
	Name   string
	Audio  []string
}

// Track is an audio or subtitle track of a copy: id is "a<n>" or "s<n>".
type Track struct {
	ID              string
	Label           string
	Language        string
	Channels        int
	Role            string
	Default         bool
	Forced          bool
	HearingImpaired bool
	// Pictures: a picture (PGS) subtitle track.
	Pictures bool
}

// CopyInfo is what Playback.Describe tells of a copy.
type CopyInfo struct {
	AudioTracks    []Track
	SubtitleTracks []Track
	DurationMs     int64
	// EpisodeStartMs is the episode's start inside a copy holding
	// several, when known (-1: unknown).
	EpisodeStartMs int64
	// Trickplay is the copy's previews, nil for none.
	Trickplay *Trickplay
}

// Trickplay is a copy's seek-bar previews: tile layout and sheet count
// (MSFP video section 5.5).
type Trickplay struct {
	TileWidth, TileHeight int
	Columns, Rows         int
	IntervalMs            int
	TileCount             int
	Sheets                int
}

// PeerRef names a peer for the host's live view: its ID and name, never
// a person.
type PeerRef struct {
	ID   string
	Name string
}

// StartRequest is what the engine asks Playback.Start to serve.
type StartRequest struct {
	Copy          string
	Peer          PeerRef
	AudioTrack    string
	SubtitleTrack string // "": none shown
	StartMs       int64
	// Capabilities are what the viewer's device plays, coarsened: only
	// values MSFP lists.
	Capabilities map[string][]string
	// MaxBitrateKbps is the cap in effect.
	MaxBitrateKbps int
	// Admit counts the pairing's streams (and, for "transcode", its
	// transcodes) before the host starts any work.
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
	Handle     string
	Method     string // "direct", "remux", "transcode"
	Protocol   string // "hls" or "file"
	MimeType   string // for "file"
	PositionMs int64
	Info       map[string]any
	Segments   []Segment
}

// Segment is a skip marker (intro, credits…).
type Segment struct {
	Type    string
	StartMs int64
	EndMs   int64
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

// Limits are what a pairing may use of a server at once: streams (0 to
// 50), transcodes among them, a bitrate cap (0: none), and music's own.
type Limits struct {
	Streams         int `json:"streams"`
	Transcodes      int `json:"transcodes"`
	BitrateKbps     int `json:"bitrateKbps,omitempty"`
	AudioStreams    int `json:"audioStreams,omitempty"`
	AudioTranscodes int `json:"audioTranscodes,omitempty"`
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
