package api

import (
	"net/http"
	"strings"
	"testing"
)

// A paired server's addresses stay on this server: the browser gets this
// server's, under its ticket; others than its stream's are refused.
func TestRelayURLs(t *testing.T) {
	out := sessionJSON{ID: "play_aaaaaaaaaaaaaaaaaaaaaaaaaa", Stream: streamJSON{URL: "https://b.example.org/api/v1/stream/T1/master.m3u8", Protocol: "hls"},
		SubtitleTracks: []trackJSON{{ID: "s3", URL: "https://b.example.org/api/v1/stream/T1/subtitles/s3"},
			{ID: "s4", Pictures: true, URL: "https://b.example.org/api/v1/stream/T1/pictures/s4"}}}
	base, ok := keepPeerURLs(&out, "https://b.example.org")
	if !ok || base != "https://b.example.org/api/v1/stream/T1/" {
		t.Fatalf("%q %v", base, ok)
	}
	relayURLs(&out, base, "LOCAL")
	if out.Stream.URL != "/api/v1/stream/LOCAL/master.m3u8" || out.SubtitleTracks[0].URL != "/api/v1/stream/LOCAL/subtitles/s3" ||
		out.SubtitleTracks[1].URL != "/api/v1/stream/LOCAL/pictures/s4" {
		t.Fatalf("%+v", out)
	}
	for _, u := range []string{"https://b.example.org/api/v1/stream/T2/subtitles/s3", "https://b.example.org/api/v1/stream/T1/x/y/z",
		"https://c.example.org/api/v1/stream/T1/subtitles/s3",
		"https://b.example.org/api/v1/stream/T1/..", "https://b.example.org/api/v1/stream/T1/.",
		"https://b.example.org/api/v1/stream/T1/subtitles/..", "https://b.example.org/api/v1/stream/T1/pictures/.",
		// Another track's, or as pictures when they're text (video section 6.1).
		"https://b.example.org/api/v1/stream/T1/subtitles/s4", "https://b.example.org/api/v1/stream/T1/pictures/s3"} {
		bad := sessionJSON{ID: out.ID, Stream: streamJSON{URL: "https://b.example.org/api/v1/stream/T1/file", Protocol: "file"},
			SubtitleTracks: []trackJSON{{ID: "s3", URL: u}}}
		if _, ok := keepPeerURLs(&bad, "https://b.example.org"); ok {
			t.Errorf("accepted %s", u)
		}
	}
}

// A failed relay's error, which gets logged, never holds the paired
// server's stream URL: it carries the ticket (MSFP media).
func TestRelayErrorWithoutTicket(t *testing.T) {
	_, err := http.Get("http://127.0.0.1:1/api/v1/stream/SECRETTICKET/file")
	if err == nil || !strings.Contains(err.Error(), "SECRETTICKET") {
		t.Fatalf("Go's error should hold the URL: %v", err)
	}
	if got := withoutURL(err).Error(); strings.Contains(got, "SECRETTICKET") || !strings.Contains(got, "Get") {
		t.Errorf("logged as %q", got)
	}
}

// A stream URL names the file its protocol gives (media section 1):
// master.m3u8 for hls, file for file, nothing else.
func TestRelayStreamNames(t *testing.T) {
	for _, c := range []struct {
		name, protocol string
		ok             bool
	}{{"master.m3u8", "hls", true}, {"file", "file", true}, {"file", "hls", false}, {"master.m3u8", "file", false},
		{"v0.m3u8", "hls", false}, {"seg1.mp4", "file", false}, {"master.m3u8", "dash", false}} {
		out := sessionJSON{ID: "play_aaaaaaaaaaaaaaaaaaaaaaaaaa", Stream: streamJSON{URL: "https://b.example.org/api/v1/stream/T1/" + c.name, Protocol: c.protocol}}
		if _, ok := keepPeerURLs(&out, "https://b.example.org"); ok != c.ok {
			t.Errorf("%s as %s: %v", c.name, c.protocol, ok)
		}
	}
}
