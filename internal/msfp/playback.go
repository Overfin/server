package msfp

import (
	"regexp"
	"strconv"
	"strings"
)

// Playback (MSFP playback, video section 5) and its media's URLs (media
// section 1, video section 6.1): the forms of what a play answer gives,
// as the viewer's server checks them.

var (
	playIDRe = regexp.MustCompile(`^play_[a-z2-7]{26}$`)
	idRe     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// PlayID says whether s is a playback ID (playback section 2.3).
func PlayID(s string) bool { return playIDRe.MatchString(s) }

// ID says whether s has the form of a library's, a track's or a
// version's ID (catalog section 2, playback section 2.3), and of a
// ticket (media section 1): 1 to 64 characters of A–Z a–z 0–9 _ -.
func ID(s string) bool { return idRe.MatchString(s) }

// TrackID reads a track ID of the given kind ('a' for audio, 's' for
// subtitles; playback section 2.1, video section 5.1): the letter and a
// number from 0 to 999 without leading zeros (a01 is malformed); false
// for anything else.
func TrackID(id string, kind byte) (int, bool) {
	if len(id) < 2 || len(id) > 4 || id[0] != kind || strings.Trim(id[1:], "0123456789") != "" || len(id) > 2 && id[1] == '0' {
		return 0, false
	}
	n, err := strconv.Atoi(id[1:])
	return n, err == nil
}

// StreamRoot is a server's stream root (media section 1) at address,
// without a discovery document: <address>/api/v1/stream/.
func StreamRoot(address string) string {
	return strings.TrimSuffix(address, "/") + "/api/v1/stream/"
}

// StreamBase checks a play answer's stream URL against the owning
// server's stream root (media section 1): <root><ticket>/master.m3u8 for
// hls, <root><ticket>/file for file, compared exactly as sent (no %, no
// query or fragment, no empty, "." or ".." part). It gives the stream's
// base, <root><ticket>/, under which every other URL of the answer must
// be.
func StreamBase(url, root, protocol string) (string, bool) {
	rest, ok := strings.CutPrefix(url, root)
	if !ok {
		return "", false
	}
	ticket, name, ok := strings.Cut(rest, "/")
	want := map[string]string{"hls": "master.m3u8", "file": "file"}[protocol]
	if !ok || !ID(ticket) || want == "" || name != want {
		return "", false
	}
	return root + ticket + "/", true
}

// SubtitleURL is a subtitle track's URL under a stream's base (video
// section 6.1): subtitles/<track> for text, pictures/<track> for PGS.
func SubtitleURL(base, track string, pictures bool) string {
	if pictures {
		return base + "pictures/" + track
	}
	return base + "subtitles/" + track
}

// SheetURL is the URL of sheet n of a playback's previews (video
// section 6.1).
func SheetURL(base string, n int) string { return base + "trickplay/" + strconv.Itoa(n) }

// Trickplay is a playback's seek-bar previews, as play's answer gives
// them (video section 5.5).
type Trickplay struct {
	TileWidth, TileHeight int
	Columns, Rows         int
	IntervalMs            int
	TileCount             int
	Sheets                int // how many sheet URLs the answer lists
}

// The bounds of video section 5.5.
const (
	maxTile       = 2048
	maxGrid       = 100
	maxSheetSide  = 4096
	MaxSheets     = 1000
	minInterval   = 100
	maxInterval   = 3_600_000
	MaxSheetBytes = 8 << 20 // a sheet, as read (media section 2.4)
)

// SheetCount is how many sheets the tiles fill: ⌈tileCount / (columns ×
// rows)⌉, 0 when either is 0.
func (t Trickplay) SheetCount() int {
	per := t.Columns * t.Rows
	if per <= 0 || t.TileCount <= 0 {
		return 0
	}
	return (t.TileCount + per - 1) / per
}

// InBounds says whether previews are within video section 5.5's bounds,
// one sheet listed for each the tiles fill: else the viewer's server
// plays on without them.
func (t Trickplay) InBounds() bool {
	in := func(v, lo, hi int) bool { return v >= lo && v <= hi }
	if !in(t.TileWidth, 1, maxTile) || !in(t.TileHeight, 1, maxTile) || !in(t.Columns, 1, maxGrid) ||
		!in(t.Rows, 1, maxGrid) || t.Columns*t.TileWidth > maxSheetSide || t.Rows*t.TileHeight > maxSheetSide ||
		!in(t.IntervalMs, minInterval, maxInterval) || t.TileCount < 1 {
		return false
	}
	n := t.SheetCount()
	return n <= MaxSheets && t.Sheets == n
}

// RelayedMediaType is the Content-Type a viewer's server gives the file
// or a segment it relays (media section 3): the owning server's when it
// is a video/ or audio/ type made of letters, digits and .+- only (never a
// playlist's), application/octet-stream otherwise.
func RelayedMediaType(ct string) string {
	t, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(ct)), ";")
	t = strings.TrimSpace(t)
	kind, sub, ok := strings.Cut(t, "/")
	if ok && (kind == "video" || kind == "audio") && sub != "" &&
		strings.Trim(sub, "abcdefghijklmnopqrstuvwxyz0123456789.+-") == "" && !strings.Contains(sub, "mpegurl") {
		return t
	}
	return "application/octet-stream"
}
