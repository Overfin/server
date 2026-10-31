// Package tracks reads a file's audio and subtitle tracks the way the
// player lists them: each language named in itself (lang.Name), with its
// region when the file says it, in its tag or in the track's title ("VFQ",
// "Latino"); one audio entry per language variant, the most channels;
// commentaries and audio descriptions apart; one subtitle entry per
// language, text first; forced subtitles apart, shown by themselves.
package tracks

import (
	"cmp"
	"slices"
	"strings"

	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/lang"
)

// Roles of audio tracks besides the film's own sound.
const (
	Main        = "main"
	Commentary  = "commentary"
	Description = "description" // for people who can't see
)

// Track is an audio or subtitle track as the player lists it.
type Track struct {
	Index    int    // the file's stream index
	Language string // a tag, with its region when known ("fr-CA"); "" unknown
	Name     string // the language's name in itself (lang.Name)
	// Audio: its channels, its role.
	Channels int
	Role     string
	// Subtitles: forced (only the lines in another language), for the hard
	// of hearing, text (else pictures: PGS).
	Forced, HearingImpaired, Text bool
	// Default: the file marks it so.
	Default bool
}

// Read sorts a file's streams into the audio tracks and the subtitles the
// player offers, and the forced subtitles it shows by itself.
func Read(streams []jellyfin.MediaStream) (audio, subtitles, forced []Track) {
	for _, s := range streams {
		switch s.Type {
		case "Audio":
			t := track(s)
			t.Channels, t.Role = s.Channels, role(s)
			audio = append(audio, t)
		case "Subtitle":
			if !s.IsTextSubtitleStream && !isPGS(s.Codec) {
				continue // pictures in a format Overfin can't show (DVD)
			}
			t := track(s)
			t.Forced, t.HearingImpaired, t.Text = s.IsForced || mentions(s.Title, "forced", "forcé", "forcés", "forcée", "forcées"), s.IsHearingImpaired || isSDH(s.Title), s.IsTextSubtitleStream
			if t.Forced {
				forced = append(forced, t)
			} else {
				subtitles = append(subtitles, t)
			}
		}
	}
	return bestAudio(audio), bestSubtitles(subtitles), bestSubtitles(forced)
}

// track is what audio and subtitles share: the language, its name, whether
// it's the file's default.
func track(s jellyfin.MediaStream) Track {
	language := lang.Tag(s.Language)
	if r := region(language, s.Title); r != "" {
		language = r
	}
	return Track{Index: s.Index, Language: language, Name: lang.Name(cmp.Or(language, s.Language)), Default: s.IsDefault}
}

// markers are the words in a track's title that say its language's
// variant, as releases write them: French dubbed in Quebec or in France,
// Spanish in Latin America or Spain, Portuguese in Brazil or Portugal.
var markers = []struct {
	language, tag string
	words         []string
}{
	{"fr", "fr-CA", []string{"vfq", "québec", "quebec", "québécois", "quebecois", "canadien", "canadian", "fr-ca"}},
	{"fr", "fr-FR", []string{"vff", "vf2", "france", "parisian", "truefrench", "fr-fr"}},
	{"es", "es-419", []string{"latino", "latinoamérica", "latinoamerica", "latin america", "latam", "es-419", "es-la"}},
	{"es", "es-ES", []string{"castellano", "castilian", "españa", "spain", "es-es"}},
	{"pt", "pt-BR", []string{"brasil", "brazil", "brazilian", "pt-br"}},
	{"pt", "pt-PT", []string{"portugal", "european", "pt-pt"}},
}

// region is a language's variant as the title says it ("VFQ" → fr-CA);
// "" when it says none, or the language already has one.
func region(language, title string) string {
	if language == "" || strings.Contains(language, "-") {
		return ""
	}
	for _, m := range markers {
		if m.language == language && mentions(title, m.words...) {
			return m.tag
		}
	}
	return ""
}

// role is what an audio track is for: the film's sound, a commentary, an
// audio description.
func role(s jellyfin.MediaStream) string {
	switch {
	case mentions(s.Title, "commentary", "commentaire", "comentario", "kommentar", "commento"):
		return Commentary
	case mentions(s.Title, "audio description", "audiodescription", "audio-description", "descriptive", "described", "visually impaired",
		"audiodescripción", "audiodeskription"):
		return Description
	}
	return Main
}

func isSDH(title string) bool {
	return mentions(title, "sdh", "hearing impaired", "malentendant", "cc")
}

func isPGS(codec string) bool {
	switch strings.ToLower(codec) {
	case "pgssub", "hdmv_pgs_subtitle", "pgs":
		return true
	}
	return false
}

// mentions says whether title holds one of words, whole, case and
// punctuation aside ("[VFQ]", "Français - VFQ").
func mentions(title string, words ...string) bool {
	t := " " + strings.Join(strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return r == ' ' || r == '[' || r == ']' || r == '(' || r == ')' || r == ',' || r == ':' || r == '|' || r == '/' || r == '.'
	}), " ") + " "
	for _, w := range words {
		if strings.Contains(t, " "+w+" ") {
			return true
		}
	}
	return false
}

// bestAudio keeps one track per language variant and role, the most
// channels (the file's default, on a tie), in the file's order.
func bestAudio(all []Track) []Track {
	var out []Track
	for _, t := range all {
		i := slices.IndexFunc(out, func(o Track) bool { return o.Language == t.Language && o.Role == t.Role && t.Language != "" })
		switch {
		case i < 0:
			out = append(out, t)
		case t.Channels > out[i].Channels || (t.Channels == out[i].Channels && t.Default && !out[i].Default):
			out[i] = t
		}
	}
	return out
}

// bestSubtitles keeps one track per language variant: text before
// pictures, then not for the hard of hearing before for them, in the
// file's order.
func bestSubtitles(all []Track) []Track {
	score := func(t Track) int {
		s := 0
		if t.Text {
			s += 2
		}
		if !t.HearingImpaired {
			s++
		}
		return s
	}
	var out []Track
	for _, t := range all {
		i := slices.IndexFunc(out, func(o Track) bool { return o.Language == t.Language && t.Language != "" })
		switch {
		case i < 0:
			out = append(out, t)
		case score(t) > score(out[i]):
			out[i] = t
		}
	}
	return out
}
