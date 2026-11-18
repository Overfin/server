package msfp

import "testing"

// The tables of MSFP playback section 2.6 and video section 5.4, row by
// row.

func TestChooseCopy(t *testing.T) {
	copies := []Copy{{Handle: "en", Audio: []string{"en"}}, {Handle: "frca", Audio: []string{"fr-CA"}}, {Handle: "fr", Audio: []string{"fr"}}}
	version := func(c Copy) string { return "v-" + c.Handle }
	for _, c := range []struct {
		version, language, want string
	}{
		{"v-fr", "en", "fr"},   // a version of the title: that copy, whatever the language
		{"v-none", "fr", "fr"}, // no copy has it: by language, same tag first
		{"", "fr-CA", "frca"},  // same tag
		{"", "fr-FR", "frca"},  // then same base language: the first
		{"", "de", "en"},       // none matches: the first
		{"", "", "en"},         // nothing asked: the first
		{"v-none", "", "en"},   // a version no copy has, no language
		{"", "FR-ca", "frca"},  // tags compared without case
		{"v-frca", "", "frca"}, // a version alone
		{"V-fr", "", "en"},     // version IDs compared exactly
	} {
		if got := copies[chooseCopy(copies, version, c.version, c.language)].Handle; got != c.want {
			t.Errorf("version %q, language %q: %s, want %s", c.version, c.language, got, c.want)
		}
	}
}

func TestChooseAudio(t *testing.T) {
	tracks := []Track{{ID: "a0", Language: "en"}, {ID: "a1", Language: "fr", Role: "commentary"},
		{ID: "a2", Language: "fr-CA", Default: true}, {ID: "a3", Language: "fr"}}
	for _, c := range []struct {
		track, language, want string
	}{
		{"a3", "en", "a3"},  // a track of the copy: that one, the language ignored
		{"a9", "en", "a0"},  // not a track of the copy: as if absent
		{"", "fr", "a3"},    // same tag, never a commentary
		{"", "fr-FR", "a2"}, // same base language: the first of the title's own sound
		{"", "de", "a2"},    // none matches: the default
		{"", "", "a2"},
	} {
		if got := chooseAudio(tracks, c.track, c.language); got != c.want {
			t.Errorf("track %q, language %q: %s, want %s", c.track, c.language, got, c.want)
		}
	}
	if got := chooseAudio([]Track{{ID: "a0"}, {ID: "a1"}}, "", "de"); got != "a0" {
		t.Errorf("no default: %s, want the first", got)
	}
}

func TestChooseSubtitles(t *testing.T) {
	tracks := []Track{{ID: "s0", Language: "en"}, {ID: "s1", Language: "fr", Forced: true}, {ID: "s2", Language: "fr-CA"},
		{ID: "s3", Language: "de", Forced: true}}
	for _, c := range []struct {
		track, language, want string
	}{
		{"off", "en", ""},  // off: none, the language ignored
		{"s3", "en", "s3"}, // a track of the copy
		{"s9", "en", "s0"}, // not one: as if absent
		{"", "fr", "s2"},   // same base language, a forced one last
		{"", "fr-CA", "s2"},
		{"", "de", "s3"}, // only a forced one matches
		{"", "it", ""},   // none matches: none shown
		{"", "", ""},
	} {
		if got := chooseSubtitles(tracks, c.track, c.language); got != c.want {
			t.Errorf("track %q, language %q: %q, want %q", c.track, c.language, got, c.want)
		}
	}
	if got := forcedFor(tracks, "fr-CA"); got != "s1" {
		t.Errorf("forced for fr-CA audio: %q", got)
	}
	if got := forcedFor(tracks, "en"); got != "" {
		t.Errorf("forced for en audio: %q", got)
	}
}

func TestCoarsen(t *testing.T) {
	c := coarsen(Capabilities{Containers: []string{"MP4", "mkv", "mp4", "avi"}, VideoCodecs: []string{"h264", "H265"},
		MaxAudioChannels: 7, MaxHeight: 1079, MaxBitrateKbps: 8999}, true)
	if len(c.Containers) != 2 || c.Containers[0] != "mkv" || c.Containers[1] != "mp4" || len(c.VideoCodecs) != 1 ||
		c.MaxAudioChannels != 6 || c.MaxHeight != 720 || c.MaxBitrateKbps != 8000 {
		t.Errorf("coarsened: %+v", c)
	}
	if c := coarsen(Capabilities{MaxAudioChannels: 1, MaxHeight: 100, MaxBitrateKbps: 999}, true); c.MaxAudioChannels != 2 ||
		c.MaxHeight != 480 || c.MaxBitrateKbps != 0 {
		t.Errorf("coarsened low: %+v", c)
	}
	if c := coarsen(Capabilities{}, true); c != nil {
		t.Errorf("nothing said: %+v", c)
	}
	if roundKbps(1999) != 1000 || roundKbps(999) != 999 || roundKbps(0) != 0 {
		t.Error("the request's bitrate")
	}
}

func TestParseRange(t *testing.T) {
	for h, want := range map[string]*Range{
		"bytes=0-99": {First: 0, Last: 99}, "bytes=5-": {First: 5, Last: -1}, "bytes=-10": {Suffix: 10},
		"bytes=000-009": {First: 0, Last: 9},
		"bytes=0-1,5-9": nil, "bytes=-": nil, "bytes=5-1": nil, "items=0-9": nil, "bytes = 0-9": nil,
		"bytes=0-1234567890123456": nil, "bytes=-0": nil, "bytes=a-b": nil, "bytes=1-2 ": nil,
	} {
		got, ok := ParseRange(h)
		if ok != (want != nil) || ok && got != *want {
			t.Errorf("%q: %+v %v", h, got, ok)
		}
	}
}
