package wire

import (
	"bytes"
	"errors"
	"regexp"
	"slices"
	"strings"
)

// Media (MSFP media, section 3): what a server checks of the parts of a
// stream it fetches from another before relaying them: HLS playlists
// here, PGS pictures in pgs.go.

// MaxPlaylist bounds a playlist, as read: 2 MiB (MSFP media, section 2.4).
const MaxPlaylist = 2 << 20

var fileNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// FileName says whether s may name a file of a stream (MSFP media): the
// characters of fileNameRe, never "." or "..", which a URL resolves as a
// folder, not a file.
func FileName(s string) bool {
	return s != "." && s != ".." && fileNameRe.MatchString(s)
}

// MaxPlaylistURIs bounds the URIs of a paired server's playlist (MSFP
// media, section 3.1): URI lines and URI= values together.
const MaxPlaylistURIs = 50_000

// Tags of the HLS subset (MSFP media, section 3.1), by playlist kind:
// what a relayed playlist keeps; other tags and comments are dropped.
var (
	MasterSubset = []string{"#EXTM3U", "#EXT-X-VERSION", "#EXT-X-INDEPENDENT-SEGMENTS", "#EXT-X-STREAM-INF"}
	MediaSubset  = []string{"#EXTM3U", "#EXT-X-VERSION", "#EXT-X-TARGETDURATION", "#EXT-X-MEDIA-SEQUENCE",
		"#EXT-X-PLAYLIST-TYPE", "#EXT-X-INDEPENDENT-SEGMENTS", "#EXTINF", "#EXT-X-DISCONTINUITY", "#EXT-X-ENDLIST", "#EXT-X-MAP"}
)

var errPlaylist = errors.New("a playlist outside what MSFP allows")

// CheckPlaylist checks a paired server's playlist as MSFP media
// section 3.1 says, in its order: at most 2 MiB; lines split at line
// feeds, one carriage return at a line's end removed; a first line of
// exactly #EXTM3U; every other line empty, a tag (or comment) whose every
// URI=, in any case and anywhere, is a quoted file name of the stream, or
// a file name alone; no EXT-X-DEFINE, EXT-X-KEY or EXT-X-SESSION-KEY; at
// most MaxPlaylistURIs URIs.
func CheckPlaylist(body []byte) error {
	_, err := FilterPlaylist(body, nil)
	return err
}

// FilterPlaylist is a paired server's playlist once checked
// (CheckPlaylist), keeping only the tags of subset, comments
// dropped, each line ending in a line feed; with a nil subset, every
// line is kept.
func FilterPlaylist(body []byte, subset []string) ([]byte, error) {
	if len(body) > MaxPlaylist {
		return nil, errPlaylist
	}
	lines := strings.Split(string(body), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSuffix(line, "\r")
		// Players end lines at a carriage return, or at U+2028, too:
		// such a character inside a line would hide what follows it
		// from these checks, as another line players read.
		if strings.ContainsFunc(lines[i], lineBreaking) {
			return nil, errors.New("a control character inside a line")
		}
	}
	if lines[0] != "#EXTM3U" {
		return nil, errors.New("not a playlist")
	}
	var out strings.Builder
	uris := 0
	for _, line := range lines {
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "#"):
			n, ok := quotedURIs(line)
			if !ok {
				return nil, errors.New("an address that isn't the stream's")
			}
			if line == "#EXT-X-DEFINE" || strings.HasPrefix(line, "#EXT-X-DEFINE:") ||
				strings.HasPrefix(line, "#EXT-X-KEY:") || strings.HasPrefix(line, "#EXT-X-SESSION-KEY:") {
				return nil, errors.New("variables or keys, which MSFP leaves out")
			}
			uris += n
			tag, _, _ := strings.Cut(line, ":")
			if subset != nil && !slices.Contains(subset, tag) {
				continue
			}
		case !FileName(line):
			return nil, errors.New("an address that isn't the stream's")
		default:
			uris++
		}
		if uris > MaxPlaylistURIs {
			return nil, errors.New("too many addresses")
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return []byte(out.String()), nil
}

// URILines are a checked playlist's URI lines.
func URILines(playlist []byte) []string {
	var out []string
	for _, line := range strings.Split(string(playlist), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out
}

// lineBreaking says whether a character may end a line for some player,
// or hide what follows it: control characters but tabs, and Unicode's
// line breaks.
func lineBreaking(r rune) bool {
	return r < 0x20 && r != '\t' || r == 0x7f || r == 0x85 || r == 0x2028 || r == 0x2029
}

// quotedURIs counts a tag line's URI= values, false when one isn't a
// quoted file name of the stream: every URI=, in any case and anywhere
// in the line (inside another attribute's quoted string too), followed by
// a quote, a file name and a quote. HLS quoted strings have no escapes.
func quotedURIs(line string) (int, bool) {
	// Upper case, ASCII letters only, byte for byte: offsets stay the
	// line's (strings.ToUpper may change a line's length).
	upper := []byte(line)
	for i, c := range upper {
		if 'a' <= c && c <= 'z' {
			upper[i] = c - 'a' + 'A'
		}
	}
	n := 0
	for from := 0; ; {
		i := bytes.Index(upper[from:], []byte("URI="))
		if i < 0 {
			return n, true
		}
		rest := line[from+i+len("URI="):]
		if !strings.HasPrefix(rest, `"`) {
			return n, false
		}
		name, _, closed := strings.Cut(rest[1:], `"`)
		if !closed || !FileName(name) {
			return n, false
		}
		n++
		from += i + len("URI=")
	}
}
