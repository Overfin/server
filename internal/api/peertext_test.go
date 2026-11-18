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
	// Track IDs of another form than a<n> and s<n>, n without leading
	// zeros (MSFP playback section 2.3): those tracks left out.
	out = sessionJSON{Method: "direct",
		AudioTracks:    []trackJSON{{ID: "a01", Label: "A"}, {ID: "s1", Label: "B"}, {ID: "a10", Label: "C"}},
		SubtitleTracks: []trackJSON{{ID: "s007", Label: "D"}, {ID: "a1", Label: "E"}, {ID: "s0", Label: "F"}},
		ForcedTracks:   []trackJSON{{ID: "x", Label: "G"}, {ID: "s2", Label: "H"}}}
	if !cleanPeerText(&out) || len(out.AudioTracks) != 1 || out.AudioTracks[0].ID != "a10" ||
		len(out.SubtitleTracks) != 1 || out.SubtitleTracks[0].ID != "s0" || len(out.ForcedTracks) != 1 || out.ForcedTracks[0].ID != "s2" {
		t.Errorf("track IDs: %+v", out)
	}
	if cleanPeerText(&sessionJSON{Method: "<script>"}) {
		t.Error("an unknown method kept")
	}
}
