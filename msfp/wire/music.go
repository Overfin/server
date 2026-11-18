package wire

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Music titles (MSFP music, sections 2 and 3): their keys, and what a
// server keeps of a music title a peer lists in titles (catalog section
// 3.3, music section 3.2).

// mbidRe is a MusicBrainz ID in its one form: lowercase 8-4-4-4-12 hex.
var mbidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// MBID says whether s is a MusicBrainz ID in the form music section 2
// gives: 36 characters, lowercase.
func MBID(s string) bool { return mbidRe.MatchString(s) }

// MusicKey is a music title key, parsed: artist/mb/<mbid>,
// album/mb/<mbid> (a release group), track/mb/<mbid> (a recording).
type MusicKey struct {
	Kind string // artist, album or track
	MBID string
}

// ParseMusicKey reads a music title key in its one canonical form; false
// for anything else, an MBID in uppercase included.
func ParseMusicKey(key string) (MusicKey, bool) {
	kind, rest, ok := strings.Cut(key, "/")
	if !ok || (kind != "artist" && kind != "album" && kind != "track") {
		return MusicKey{}, false
	}
	id, ok := strings.CutPrefix(rest, "mb/")
	if !ok || !MBID(id) {
		return MusicKey{}, false
	}
	return MusicKey{Kind: kind, MBID: id}, true
}

// String is the key, as ParseMusicKey reads it.
func (k MusicKey) String() string { return k.Kind + "/mb/" + k.MBID }

// Playable says whether play takes the title: a track (music section 5).
func (k MusicKey) Playable() bool { return k.Kind == "track" }

// TitleKey names a title of any content part (catalog section 4): its
// part (PartVideo or PartMusic), its kind, and whether play takes it.
type TitleKey struct {
	Part     string
	Kind     string
	Playable bool
}

// ParseKey reads a title key of either content part of version 1, in
// its canonical form only; false for any other text.
func ParseKey(key string) (TitleKey, bool) {
	if k, ok := ParseVideoKey(key); ok {
		return TitleKey{Part: PartVideo, Kind: k.Kind, Playable: k.Playable()}, true
	}
	if k, ok := ParseMusicKey(key); ok {
		return TitleKey{Part: PartMusic, Kind: k.Kind, Playable: k.Playable()}, true
	}
	return TitleKey{}, false
}

// The lengths of a music title's text, in code points (music section 3).
const (
	MaxMusicName    = 256
	MaxArtistNames  = 512
	MaxMusicArtists = 16
)

// CleanText cleans a peer's text as MSFP core section 9.1 cleans a
// server's name, cut to max code points: each run of white space one
// space, other control and format characters removed, both ends trimmed,
// cut, and trimmed again. Text, never markup: whoever shows it escapes it.
func CleanText(s string, max int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.Is(unicode.White_Space, r) }), " ")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max]))
	}
	return s
}

// Other identifiers of a music title (music section 3.1), by scheme, and
// the kinds that carry each.
var musicIDs = map[string]struct {
	re    *regexp.Regexp
	kinds []string
}{
	"mbrelease": {mbidRe, []string{"album", "track"}},
	"mbtrack":   {mbidRe, []string{"track"}},
	"isrc":      {regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{3}[0-9]{7}$`), []string{"track"}},
}

// MusicID says whether value is an identifier of a known scheme for a
// title of kind, in its form.
func MusicID(kind, scheme, value string) bool {
	s, known := musicIDs[scheme]
	return known && s.re.MatchString(value) && slices.Contains(s.kinds, kind)
}

// MusicTitle is a peer's music title, as kept: its key, its kind, the
// library it gives, and its description, cleaned. Numbers are 0 when
// absent.
type MusicTitle struct {
	Key         string
	Kind        string
	Library     string
	Name        string
	Artists     []string
	ArtistNames string
	Album       string
	Disc        int
	Number      int
	Year        int
	DurationMs  int64 // 0: none (a durationMs of 0 counts as absent)
	IDs         map[string]string
}

// ReadMusicTitle reads one entry of a titles answer as catalog section
// 3.3 and music section 3.2 say: false, to skip it, when it isn't an
// object whose key, kind and library are strings, when the key isn't a
// canonical music key, when its kind isn't the key's, or when it has no
// name once cleaned. Every other member of the wrong type, out of range
// or of another kind counts as absent; a bad entry of artists or ids is
// skipped alone. Whether library is one asked for is the caller's to
// check.
func ReadMusicTitle(raw json.RawMessage) (MusicTitle, bool) {
	var t struct {
		Key         string          `json:"key"`
		Kind        string          `json:"kind"`
		Library     string          `json:"library"`
		Name        json.RawMessage `json:"name"`
		Artists     json.RawMessage `json:"artists"`
		ArtistNames json.RawMessage `json:"artistNames"`
		Album       json.RawMessage `json:"album"`
		Disc        json.RawMessage `json:"disc"`
		Number      json.RawMessage `json:"number"`
		Year        json.RawMessage `json:"year"`
		DurationMs  json.RawMessage `json:"durationMs"`
		IDs         json.RawMessage `json:"ids"`
	}
	if DecodeEntry(raw, &t) != nil {
		return MusicTitle{}, false
	}
	k, ok := ParseMusicKey(t.Key)
	if !ok || k.Kind != t.Kind {
		return MusicTitle{}, false
	}
	out := MusicTitle{Key: t.Key, Kind: t.Kind, Library: t.Library}
	var name string
	if json.Unmarshal(t.Name, &name) != nil {
		return MusicTitle{}, false
	}
	if out.Name = CleanText(name, MaxMusicName); out.Name == "" {
		return MusicTitle{}, false
	}
	credited := k.Kind == "album" || k.Kind == "track"
	if credited {
		for _, s := range stringsOf(t.Artists) {
			if len(out.Artists) == MaxMusicArtists {
				break
			}
			if a, ok := ParseMusicKey(s); ok && a.Kind == "artist" && !slices.Contains(out.Artists, s) {
				out.Artists = append(out.Artists, s)
			}
		}
		var names string
		if json.Unmarshal(t.ArtistNames, &names) == nil {
			out.ArtistNames = CleanText(names, MaxArtistNames)
		}
		out.Year = int(integerIn(t.Year, 1000, 9999))
	}
	if k.Kind == "track" {
		var album string
		if json.Unmarshal(t.Album, &album) == nil {
			if a, ok := ParseMusicKey(album); ok && a.Kind == "album" {
				out.Album = album
			}
		}
		out.Disc = int(integerIn(t.Disc, 1, 999))
		out.Number = int(integerIn(t.Number, 1, 999))
		out.DurationMs = max(integerIn(t.DurationMs, 0, 86_400_000), 0)
	}
	var ids map[string]any // not an object: none
	if json.Unmarshal(t.IDs, &ids) == nil {
		for scheme, v := range ids {
			if value, ok := v.(string); ok && MusicID(k.Kind, scheme, value) {
				if out.IDs == nil {
					out.IDs = map[string]string{}
				}
				out.IDs[scheme] = value
			}
		}
	}
	return out, true
}

// integerIn reads an MSFP integer (core section 2.3: no fraction, no
// exponent, no minus zero) within lo to hi; 0 otherwise, or -1 when lo
// is 0 and it isn't one.
func integerIn(raw json.RawMessage, lo, hi int64) int64 {
	none := int64(0)
	if lo == 0 {
		none = -1
	}
	s := string(raw)
	if !isInteger(s) || s == "-0" {
		return none
	}
	var n int64
	if json.Unmarshal(raw, &n) != nil || n < lo || n > hi {
		return none
	}
	return n
}
