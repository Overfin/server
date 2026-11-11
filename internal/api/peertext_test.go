package api

import (
	"strings"
	"testing"
)

// A paired server's playback answer reaches the viewer as plain text, and
// values Overfin itself gives.
func TestCleanPeerText(t *testing.T) {
	out := sessionJSON{Method: "transcode", Stream: streamJSON{MimeType: "text/html"},
		AudioTracks:    []trackJSON{{ID: "a1", Label: "<b>Fran\u202eçais</b>", Language: "fr", Role: "evil"}, {ID: "a2", Label: "\u200b"}},
		Versions:       []versionJSON{{ID: "v", Name: "4K\nHDR"}},
		SubtitleTracks: []trackJSON{{ID: "s1", Label: "English", Language: "not a language tag"}}}
	if !cleanPeerText(&out) {
		t.Fatal("refused")
	}
	if out.Stream.MimeType != "" || len(out.AudioTracks) != 1 || strings.ContainsAny(out.AudioTracks[0].Label, "<>\u202e") ||
		out.AudioTracks[0].Role != "" || out.Versions[0].Name != "4K HDR" || out.SubtitleTracks[0].Language != "" {
		t.Errorf("cleaned: %+v", out)
	}
	if cleanPeerText(&sessionJSON{Method: "<script>"}) {
		t.Error("an unknown method kept")
	}
}
