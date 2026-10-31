package identify

import (
	"regexp"
	"strconv"
	"strings"
)

// Reading folder and file names: what a title is called there, its year,
// the IDs written in it, and an episode's numbers.

// named is what a folder or a file name says: a title, its year, and the
// IDs it carries ("[tmdbid-949]", "[imdbid-tt0113277]", "[tvdbid-81189]",
// as Jellyfin, Radarr and Sonarr write them).
type named struct {
	name             string
	year             int
	tmdb, imdb, tvdb string
}

var (
	tmdbTag   = regexp.MustCompile(`(?i)[\[{(]\s*tmdb(?:id)?\s*[-=:]\s*(\d{1,9})\s*[\]})]`)
	imdbTag   = regexp.MustCompile(`(?i)[\[{(]\s*(?:imdb(?:id)?\s*[-=:]\s*)?(tt\d{5,10})\s*[\]})]`)
	tvdbTag   = regexp.MustCompile(`(?i)[\[{(]\s*tvdb(?:id)?\s*[-=:]\s*(\d{1,9})\s*[\]})]`)
	brackets  = regexp.MustCompile(`[\[{][^\]}]*[\]}]`)
	extension = regexp.MustCompile(`\.[A-Za-z0-9]{2,4}$`)
	yearIn    = regexp.MustCompile(`(?:^|[\s._(\[-])((?:19|20)\d{2})(?:$|[\s._)\]-])`)
	// group is a release group at the end of a scene name ("-SPARKS").
	group = regexp.MustCompile(`-[A-Za-z0-9]{2,15}$`)
)

// releaseTags are words of a release's description, never of a title's
// name: where a name without a year stops ("The.Candidate.1080p.WEB").
// Languages and editions aren't here: "The French Connection",
// "Extended" are names too.
var releaseTags = map[string]bool{
	"480p": true, "576p": true, "720p": true, "1080p": true, "1080i": true, "2160p": true, "4k": true, "uhd": true,
	"bluray": true, "blu-ray": true, "bdrip": true, "brrip": true, "bdremux": true, "remux": true, "web": true, "web-dl": true,
	"webdl": true, "webrip": true, "hdtv": true, "hdrip": true, "dvdrip": true, "dvdr": true, "dvd": true,
	"x264": true, "x265": true, "h264": true, "h265": true, "h.264": true, "h.265": true, "hevc": true, "avc": true,
	"av1": true, "xvid": true, "divx": true, "10bit": true, "hdr": true, "hdr10": true, "dv": true,
	"aac": true, "ac3": true, "dts": true, "truehd": true, "atmos": true, "flac": true, "ddp": true, "dd5": true,
	"multi": true, "vostfr": true, "truefrench": true, "subfrench": true, "vff": true, "vfq": true,
}

// parse reads a folder or file name such as "Heat (1995) [tmdbid-949]" or
// "The.Candidate.2023.1080p.WEB-GROUP.mkv".
func parse(s string) named {
	var n named
	n.tmdb = numberID(tmdbTag, s)
	n.tvdb = numberID(tvdbTag, s)
	if m := imdbTag.FindStringSubmatch(s); m != nil {
		n.imdb = strings.ToLower(m[1])
	}
	s = brackets.ReplaceAllString(s, " ")
	s = extension.ReplaceAllString(strings.TrimSpace(s), "")
	// The year: the last one with a name before it ("1917 (2019)" is
	// 1917, from 2019; "1917" alone is a name).
	cut := len(s)
	for _, m := range yearIn.FindAllStringSubmatchIndex(s, -1) {
		if strings.Trim(s[:m[2]], " ._([-") != "" {
			n.year, _ = strconv.Atoi(s[m[2]:m[3]])
			cut = m[2]
		}
	}
	words := strings.Fields(strings.NewReplacer(".", " ", "_", " ").Replace(s[:cut]))
	if n.year == 0 {
		// Without a year, the name stops at the release's description, and
		// a scene name's group goes.
		for i, w := range words {
			if i > 0 && isReleaseTag(w) {
				words = words[:i]
				break
			}
		}
		if len(words) > 1 {
			words[len(words)-1] = group.ReplaceAllString(words[len(words)-1], "")
		}
	}
	n.name = strings.Trim(strings.Join(strings.Fields(strings.Join(words, " ")), " "), " -([")
	return n
}

// isReleaseTag says whether a word describes a release ("1080p",
// "WEB-DL", "x264-GROUP").
func isReleaseTag(w string) bool {
	w = strings.ToLower(strings.Trim(w, "()[]-"))
	if releaseTags[w] {
		return true
	}
	before, _, found := strings.Cut(w, "-")
	return found && releaseTags[before]
}

// numberID is the number tag finds in s, as TMDB and TVDB write them
// ("0949" is 949, and 0 is none); "" without one.
func numberID(tag *regexp.Regexp, s string) string {
	m := tag.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	if id, _ := strconv.Atoi(m[1]); id > 0 {
		return strconv.Itoa(id)
	}
	return ""
}

// episodeNumbers are what an episode's file name says: its season (0 when
// it doesn't say), its episode, the last of several in one file, or its
// number across the whole series (anime: "One Piece - 1071").
type episodeNumbers struct {
	season, episode, last int
	absolute              int
}

var (
	// S01E02, s1e2, S01E01E02, S01E01-E02, S01E01-02.
	seasonEpisode = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,4})[ ._-]?e(\d{1,4})(?:(?:[ ._-]?e|-)(\d{1,4}))?(?:[^0-9]|$)`)
	// 1x02.
	crossed = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(\d{1,2})x(\d{1,3})(?:[^0-9p]|$)`)
	// "Name - 1071", "Name - E1071", "Name Ep 1071", "Name #1071".
	absolute = regexp.MustCompile(`(?i)(?:\s-\s+|\s(?:ep|episode)\.?\s*|#|\s-\s*e)(\d{1,4})(?:v\d)?(?:[\s._\[(-]|$)`)
	parens   = regexp.MustCompile(`\([^)]*\)`)
)

// readEpisode reads an episode's numbers from its file name.
func readEpisode(file string) (episodeNumbers, bool) {
	file = extension.ReplaceAllString(strings.TrimSpace(file), "")
	// Episodes are numbered from 1; a season, from 0 (specials).
	if m := seasonEpisode.FindStringSubmatch(file); m != nil && atoi(m[2]) > 0 {
		e := episodeNumbers{season: atoi(m[1]), episode: atoi(m[2])}
		if m[3] != "" && atoi(m[3]) > e.episode {
			e.last = atoi(m[3])
		}
		return e, true
	}
	if m := crossed.FindStringSubmatch(file); m != nil && atoi(m[2]) > 0 {
		return episodeNumbers{season: atoi(m[1]), episode: atoi(m[2])}, true
	}
	// Brackets hold groups, resolutions, checksums: never the number.
	plain := brackets.ReplaceAllString(file, " ")
	plain = parens.ReplaceAllString(plain, " ")
	if m := absolute.FindStringSubmatch(plain); m != nil && atoi(m[1]) > 0 {
		return episodeNumbers{absolute: atoi(m[1])}, true
	}
	return episodeNumbers{}, false
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
