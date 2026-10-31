package tracks

import (
	"testing"

	"github.com/overfin/server/internal/jellyfin"
)

func names(ts []Track) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Name)
	}
	return out
}

func indexes(ts []Track) []int {
	var out []int
	for _, t := range ts {
		out = append(out, t.Index)
	}
	return out
}

func same[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Predator: Badlands' tracks, as its file has them: one audio entry per
// language, the most channels; French subtitles from the text track, not
// the pictures; English, the pictures (none in text), for the hard of
// hearing since there's nothing else; the forced French, apart, from text.
func TestPredator(t *testing.T) {
	audio, subs, forced := Read([]jellyfin.MediaStream{
		{Index: 0, Type: "Video"},
		{Index: 1, Type: "Audio", Language: "fre", Title: "FR VFQ : DD+ 5.1", Channels: 6, IsDefault: true},
		{Index: 2, Type: "Audio", Language: "eng", Title: "ENG : TrueHD Atmos 7.1", Channels: 8},
		{Index: 3, Type: "Subtitle", Language: "fre", Title: "FR Forced : SRT", IsForced: true, IsTextSubtitleStream: true},
		{Index: 4, Type: "Subtitle", Language: "fre", Title: "FR Full : SRT", IsTextSubtitleStream: true},
		{Index: 5, Type: "Subtitle", Language: "fre", Title: "FR Forced : PGS", IsForced: true, Codec: "PGSSUB"},
		{Index: 6, Type: "Subtitle", Language: "fre", Title: "FR Full : PGS", Codec: "PGSSUB"},
		{Index: 7, Type: "Subtitle", Language: "eng", Title: "ENG Full SDH : PGS", Codec: "PGSSUB"},
	})
	if !same(names(audio), []string{"Français (Canada)", "English"}) || audio[0].Language != "fr-CA" || audio[1].Channels != 8 {
		t.Errorf("audio: %+v", audio)
	}
	if !same(indexes(subs), []int{4, 7}) || !subs[0].Text || subs[1].Text || !subs[1].HearingImpaired {
		t.Errorf("subtitles: %+v", subs)
	}
	if !same(indexes(forced), []int{3}) {
		t.Errorf("forced: %+v", forced)
	}
}

// Two French dubs and an English one: each its own entry, named by the
// marker in its title; the 5.1 kept over the stereo of the same variant;
// a commentary and an audio description apart, never merged with the
// film's sound.
func TestAudio(t *testing.T) {
	audio, _, _ := Read([]jellyfin.MediaStream{
		{Index: 1, Type: "Audio", Language: "fre", Title: "[VFQ] Stéréo", Channels: 2},
		{Index: 2, Type: "Audio", Language: "fre", Title: "VFQ 5.1", Channels: 6},
		{Index: 3, Type: "Audio", Language: "fre", Title: "VFF 5.1", Channels: 6},
		{Index: 4, Type: "Audio", Language: "eng", Channels: 6},
		{Index: 5, Type: "Audio", Language: "eng", Title: "Director's Commentary", Channels: 2},
		{Index: 6, Type: "Audio", Language: "fre", Title: "Audiodescription (VFQ)", Channels: 2},
		{Index: 7, Type: "Audio", Language: "spa", Title: "Latino", Channels: 6},
		{Index: 8, Type: "Audio", Language: "spa", Title: "Castellano", Channels: 6},
		{Index: 9, Type: "Audio", Language: "und", Channels: 2},
		// Its tag says France, its title Quebec: the tag wins; 7.1 over 5.1.
		{Index: 10, Type: "Audio", Language: "fr-FR", Title: "VFQ", Channels: 8},
	})
	if !same(indexes(audio), []int{2, 10, 4, 5, 6, 7, 8, 9}) {
		t.Fatalf("kept: %v %v", indexes(audio), names(audio))
	}
	want := []struct{ name, language, role string }{
		{"Français (Canada)", "fr-CA", Main}, {"Français (France)", "fr-FR", Main}, {"English", "en", Main},
		{"English", "en", Commentary}, {"Français (Canada)", "fr-CA", Description}, {"Español (Latinoamérica)", "es-419", Main},
		{"Español (España)", "es-ES", Main}, {"?", "", Main},
	}
	for i, w := range want {
		if a := audio[i]; a.Name != w.name || a.Language != w.language || a.Role != w.role {
			t.Errorf("%d: %+v, want %+v", i, a, w)
		}
	}
}

// Markers are whole words only, and only for their own language.
func TestMarkers(t *testing.T) {
	for _, c := range []struct{ language, title, want string }{
		{"fr", "Français [VFQ]", "fr-CA"}, {"fr", "VFQuality", ""}, {"en", "VFQ", ""}, {"pt", "Português (Brasil)", "pt-BR"},
		{"fr", "", ""}, {"fr-CA", "VFF", ""},
	} {
		if got := region(c.language, c.title); got != c.want {
			t.Errorf("%q %q: %q, want %q", c.language, c.title, got, c.want)
		}
	}
}

// Subtitles: text before pictures, whole before for the hard of hearing;
// DVD pictures left out (Overfin can't show them).
func TestSubtitles(t *testing.T) {
	_, subs, forced := Read([]jellyfin.MediaStream{
		{Index: 1, Type: "Subtitle", Language: "eng", Title: "SDH", IsTextSubtitleStream: true},
		{Index: 2, Type: "Subtitle", Language: "eng", IsTextSubtitleStream: true},
		{Index: 3, Type: "Subtitle", Language: "ger", Codec: "dvdsub"},
		{Index: 4, Type: "Subtitle", Language: "fre", Codec: "pgssub"},
		{Index: 5, Type: "Subtitle", Language: "fre", Title: "Forcés", Codec: "pgssub"},
	})
	if !same(indexes(subs), []int{2, 4}) || !same(indexes(forced), []int{5}) {
		t.Errorf("subtitles %v, forced %v", indexes(subs), indexes(forced))
	}
}

// Pick: the same variant, else the same language; never a commentary.
func TestPick(t *testing.T) {
	audio, subs, _ := Read([]jellyfin.MediaStream{
		{Index: 1, Type: "Audio", Language: "eng", Title: "Commentary", Channels: 2},
		{Index: 2, Type: "Audio", Language: "fre", Title: "VFF", Channels: 6},
		{Index: 3, Type: "Audio", Language: "fre", Title: "VFQ", Channels: 6},
		{Index: 4, Type: "Subtitle", Language: "fre", IsTextSubtitleStream: true},
	})
	for _, c := range []struct {
		language string
		want     int
	}{{"fr-CA", 3}, {"fr-FR", 2}, {"fr-BE", 2}, {"fr", 2}, {"en", -1}, {"", -1}, {"xx", -1}} {
		got, ok := Pick(audio, c.language)
		if (c.want < 0 && ok) || (c.want >= 0 && got.Index != c.want) {
			t.Errorf("%q: %d %v, want %d", c.language, got.Index, ok, c.want)
		}
	}
	if s, ok := Pick(subs, "fr-CA"); !ok || s.Index != 4 {
		t.Errorf("subtitles: %+v", s)
	}
	if _, ok := Find(audio, 3); !ok {
		t.Error("find")
	}
}
