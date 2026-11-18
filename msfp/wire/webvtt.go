package wire

import (
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Text subtitles (MSFP video, section 6.3): a WebVTT part is rebuilt, not
// filtered. Only cue timings and plain cue text survive; cue settings,
// identifiers, styles, regions, notes and every tag go. The owning server
// rebuilds what it serves, the viewer's server again what it relays.

// The bounds of a WebVTT part.
const (
	MaxWebVTT  = 5 << 20 // bytes, as read
	maxCues    = 50_000
	maxCueLine = 500 // bytes, a cue text line once cleaned
)

// cueTiming is a timing line, once white space at its ends is removed:
// two timestamps ([hours:]minutes:seconds.milliseconds) around "-->",
// with spaces, tabs or form feeds between, then the cue settings.
var cueTiming = regexp.MustCompile(`^((?:\d{1,3}:)?\d{2}:\d{2}\.\d{3})[ \t\f]+-->[ \t\f]+((?:\d{1,3}:)?\d{2}:\d{2}\.\d{3})(?:[ \t\f].*)?$`)

// cueEscape escapes plain text for a cue: in WebVTT, "<" opens a tag and
// "&" a character reference.
var cueEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// RebuildWebVTT rebuilds a WebVTT part as video section 6.3 says: false
// when it is over MaxWebVTT, doesn't start with WEBVTT, or holds more than
// 50,000 cues once rebuilt.
func RebuildWebVTT(in []byte) ([]byte, bool) {
	if len(in) > MaxWebVTT {
		return nil, false
	}
	text := strings.ReplaceAll(strings.ReplaceAll(string(in), "\r\n", "\n"), "\r", "\n")
	text = strings.TrimPrefix(text, "\uFEFF") // one byte order mark
	if !strings.HasPrefix(text, "WEBVTT") {
		return nil, false
	}
	var out strings.Builder
	out.WriteString("WEBVTT\n")
	cues := 0
	for _, block := range strings.Split(text, "\n\n") {
		lines := strings.Split(strings.Trim(block, "\n"), "\n")
		// The first timing line starts the cue; what's before it (the
		// header, an identifier) goes. A block without one goes whole.
		timing := -1
		var m []string
		for i, l := range lines {
			if m = cueTiming.FindStringSubmatch(strings.TrimFunc(l, unicode.IsSpace)); m != nil {
				timing = i
				break
			}
		}
		if timing < 0 {
			continue
		}
		var body []string
		for _, l := range lines[timing+1:] {
			if c := cleanCueLine(l); c != "" {
				body = append(body, c)
			}
		}
		if len(body) == 0 {
			continue
		}
		if cues++; cues > maxCues {
			return nil, false
		}
		out.WriteString("\n" + m[1] + " --> " + m[2] + "\n" + strings.Join(body, "\n") + "\n")
	}
	return []byte(out.String()), true
}

// cleanCueLine cleans a cue text line, in video section 6.3's steps:
// invalid UTF-8, U+FFFD and direction controls removed; tags removed;
// character references decoded; both again; controls and white space
// made spaces, collapsed and trimmed; cut to 500 bytes; then escaped.
func cleanCueLine(s string) string {
	s = stripCueTags(dropInvisible(s))
	s = html.UnescapeString(s)
	s = stripCueTags(dropInvisible(s))
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }), " ")
	if len(s) > maxCueLine {
		cut := maxCueLine
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = strings.TrimRight(s[:cut], " ")
	}
	return cueEscape.Replace(s)
}

// stripCueTags removes tags: from a "<" followed by a letter (any
// script), "/", "!" or "?", to the next ">", the tag becoming one space;
// one without a ">" removes the rest of the line. Any other "<" stays.
func stripCueTags(s string) string {
	var b strings.Builder
	inTag := false
	for i, r := range s {
		switch {
		case inTag:
			if r == '>' {
				inTag = false
				b.WriteByte(' ')
			}
		case r == '<' && startsTag(s[i+1:]):
			inTag = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func startsTag(rest string) bool {
	r, _ := utf8.DecodeRuneInString(rest)
	return unicode.IsLetter(r) || r == '/' || r == '!' || r == '?'
}

// dropInvisible removes invalid UTF-8, U+FFFD and the direction controls
// (U+061C, U+200E, U+200F, U+202A to U+202E, U+2066 to U+2069).
func dropInvisible(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == utf8.RuneError, r == 0x061C, r == 0x200E, r == 0x200F,
			r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
			return -1
		}
		return r
	}, s)
}

// StretchSeconds is how long a part of subtitles covers (video section
// 6.2): part n covers n × 120 to (n + 1) × 120 seconds.
const StretchSeconds = 120

// WebVTTPart is part n of a whole WebVTT track, for an owning server
// that holds its subtitles as one file: the track rebuilt as
// RebuildWebVTT does, keeping the cues that show during the part's
// stretch, from 10 seconds before it (video section 6.2). A part with no
// cue is "WEBVTT" and a line feed. False when the track isn't WebVTT.
func WebVTTPart(track []byte, n int) ([]byte, bool) {
	all, ok := RebuildWebVTT(track)
	if !ok {
		return nil, false
	}
	from := int64(n*StretchSeconds-10) * 1000
	to := int64(n+1) * StretchSeconds * 1000
	var out strings.Builder
	out.WriteString("WEBVTT\n")
	for _, cue := range strings.Split(string(all), "\n\n")[1:] {
		timing, _, _ := strings.Cut(cue, "\n")
		m := cueTiming.FindStringSubmatch(timing)
		if m != nil && cueMs(m[2]) > from && cueMs(m[1]) < to {
			out.WriteString("\n" + cue)
			if !strings.HasSuffix(cue, "\n") {
				out.WriteString("\n")
			}
		}
	}
	return []byte(out.String()), true
}

// cueMs is a cue timestamp, [hours:]minutes:seconds.milliseconds, in
// milliseconds.
func cueMs(ts string) int64 {
	var ms int64
	clock, frac, _ := strings.Cut(ts, ".")
	for _, part := range strings.Split(clock, ":") {
		n, _ := strconv.ParseInt(part, 10, 64)
		ms = ms*60 + n
	}
	f, _ := strconv.ParseInt(frac, 10, 64)
	return ms*1000 + f
}
