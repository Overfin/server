package msfp

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// Video titles (MSFP video, sections 3 and 4): their keys, and what a
// server keeps of a title a peer lists in titles (catalog section 3.3,
// video section 3.1).

// VideoKey is a video title key, parsed: movie/tmdb/949,
// series/tmdb/1399, series/tmdb/1399/s1, series/tmdb/1399/e63056 (an
// episode by TMDB's ID of it), series/tmdb/1399/s1/e2 (an episode by its
// numbers, for one TMDB doesn't list).
type VideoKey struct {
	Kind string // movie, series, season or episode
	// TMDB is the film's or the series' TMDB ID.
	TMDB int64
	// Season and Episode: a season's number, an episode's numbers when
	// it is named by them.
	Season, Episode int
	// EpisodeID is TMDB's ID of an episode named by it; 0 for one named
	// by its numbers.
	EpisodeID int64
}

// ParseVideoKey reads a video title key in its one canonical form (video
// section 4): the kind, the scheme tmdb, the TMDB ID (1 to 9,999,999,999),
// then for a season s<season> (0, or 1 to 4 digits), for an episode
// e<episode id> (1 to 10 digits) or s<season>/e<episode> (episode 0, or 1
// to 5 digits); never a leading zero. Any other key is false: another
// form of the same title is another string, which a receiver skips.
func ParseVideoKey(key string) (VideoKey, bool) {
	parts := strings.Split(key, "/")
	if len(parts) < 3 || len(parts) > 5 || parts[1] != "tmdb" {
		return VideoKey{}, false
	}
	id, ok := decimal(parts[2], 10, false)
	if !ok || id > 9_999_999_999 {
		return VideoKey{}, false
	}
	k := VideoKey{Kind: parts[0], TMDB: id}
	switch {
	case k.Kind == "movie" && len(parts) == 3, k.Kind == "series" && len(parts) == 3:
		return k, true
	case k.Kind != "series":
		return VideoKey{}, false
	}
	if digits, ok := strings.CutPrefix(parts[3], "e"); ok && len(parts) == 4 {
		k.Kind = "episode"
		k.EpisodeID, ok = decimal(digits, 10, false)
		return k, ok
	}
	digits, ok := strings.CutPrefix(parts[3], "s")
	season, okSeason := decimal(digits, 4, true)
	if !ok || !okSeason {
		return VideoKey{}, false
	}
	k.Kind, k.Season = "season", int(season)
	if len(parts) == 4 {
		return k, true
	}
	digits, ok = strings.CutPrefix(parts[4], "e")
	episode, okEpisode := decimal(digits, 5, true)
	if !ok || !okEpisode {
		return VideoKey{}, false
	}
	k.Kind, k.Episode = "episode", int(episode)
	return k, true
}

// decimal reads 1 to most digits without a leading zero; "0" itself only
// when zero is allowed.
func decimal(s string, most int, zero bool) (int64, bool) {
	if s == "" || len(s) > most || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	if s[0] == '0' && (len(s) > 1 || !zero) {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// String is the key, as ParseVideoKey reads it.
func (k VideoKey) String() string {
	id := strconv.FormatInt(k.TMDB, 10)
	switch {
	case k.Kind == "movie":
		return "movie/tmdb/" + id
	case k.Kind == "series":
		return "series/tmdb/" + id
	case k.Kind == "season":
		return "series/tmdb/" + id + "/s" + strconv.Itoa(k.Season)
	case k.EpisodeID > 0:
		return "series/tmdb/" + id + "/e" + strconv.FormatInt(k.EpisodeID, 10)
	}
	return "series/tmdb/" + id + "/s" + strconv.Itoa(k.Season) + "/e" + strconv.Itoa(k.Episode)
}

// Playable says whether play takes the title: a film or an episode
// (video section 5).
func (k VideoKey) Playable() bool { return k.Kind == "movie" || k.Kind == "episode" }

// Languages: a title's lists of language tags, as a receiver keeps them
// (video section 3.1): at most MaxTags, each 2 to 12 characters of
// A–Z a–z 0–9 -, each once, compared without case.
const MaxTags = 12

// LanguageTag says whether s may be a language tag of a title's lists,
// or of play's audioLanguage and subtitleLanguage (which may also be
// left out).
func LanguageTag(s string) bool {
	return len(s) >= 2 && len(s) <= 12 && strings.Trim(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-") == ""
}

// CleanTags keeps of a peer's tags those LanguageTag allows, each once,
// at most MaxTags; a bad tag is skipped alone.
func CleanTags(tags []string) []string {
	var out []string
	for _, t := range tags {
		if len(out) == MaxTags {
			break
		}
		if !LanguageTag(t) || containsFold(out, t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

func containsFold(list []string, s string) bool {
	for _, o := range list {
		if strings.EqualFold(o, s) {
			return true
		}
	}
	return false
}

// MaxWith is how many other episodes' keys an episode's with lists at
// most (video section 4.2).
const MaxWith = 16

// CleanWith keeps of an episode's with the keys of other episodes of the
// same series, each once, at most MaxWith; a bad key is skipped alone.
func CleanWith(episode VideoKey, keys []string) []string {
	var out []string
	for _, s := range keys {
		if len(out) == MaxWith {
			break
		}
		k, ok := ParseVideoKey(s)
		if !ok || k.Kind != "episode" || k.TMDB != episode.TMDB || s == episode.String() || containsFold(out, s) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// Other identifiers of a film, a series or an episode (video section
// 4.3), by scheme: IMDb's and TheTVDB's.
var otherIDs = map[string]*regexp.Regexp{
	"imdb": regexp.MustCompile(`^tt[0-9]{7,10}$`),
	"tvdb": regexp.MustCompile(`^[1-9][0-9]{0,9}$`),
}

// OtherID says whether value is an identifier of a known scheme, in its
// form: what a sender sends, and a receiver keeps.
func OtherID(scheme, value string) bool {
	re, known := otherIDs[scheme]
	return known && re.MatchString(value)
}

// VideoTitle is a peer's video title, as kept: its key, its kind, the
// library it gives, and the members video adds, cleaned.
type VideoTitle struct {
	Key       string
	Kind      string
	Library   string
	Audio     []string
	Subtitles []string
	With      []string
	IDs       map[string]string
}

// ReadVideoTitle reads one entry of a titles answer as catalog section
// 3.3 and video section 3.1 say: false, to skip it, when it isn't an
// object whose key, kind and library are strings, when the key isn't a
// canonical video key, or when its kind isn't the key's. A member video
// adds of the wrong type counts as absent; a bad entry of one of its
// lists is skipped alone. Whether library is one asked for is the
// caller's to check.
func ReadVideoTitle(raw json.RawMessage) (VideoTitle, bool) {
	var t struct {
		Key       string          `json:"key"`
		Kind      string          `json:"kind"`
		Library   string          `json:"library"`
		Audio     json.RawMessage `json:"audio"`
		Subtitles json.RawMessage `json:"subtitles"`
		With      json.RawMessage `json:"with"`
		IDs       json.RawMessage `json:"ids"`
	}
	if DecodeEntry(raw, &t) != nil {
		return VideoTitle{}, false
	}
	k, ok := ParseVideoKey(t.Key)
	if !ok || k.Kind != t.Kind {
		return VideoTitle{}, false
	}
	out := VideoTitle{Key: t.Key, Kind: t.Kind, Library: t.Library}
	// Languages for films and episodes only, with for episodes only.
	if k.Playable() {
		out.Audio, out.Subtitles = CleanTags(stringsOf(t.Audio)), CleanTags(stringsOf(t.Subtitles))
	}
	if k.Kind == "episode" {
		out.With = CleanWith(k, stringsOf(t.With))
	}
	var ids map[string]any // not an object: none
	if k.Kind != "season" && json.Unmarshal(t.IDs, &ids) == nil {
		for scheme, v := range ids {
			if value, ok := v.(string); ok && OtherID(scheme, value) {
				if out.IDs == nil {
					out.IDs = map[string]string{}
				}
				out.IDs[scheme] = value
			}
		}
	}
	return out, true
}

// stringsOf reads a list's strings: none when it isn't a list, an entry
// that isn't a string skipped.
func stringsOf(raw json.RawMessage) []string {
	var list []any
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	var out []string
	for _, v := range list {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
