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
		SubtitleTracks: []trackJSON{{URL: "https://b.example.org/api/v1/stream/T1/subtitles/s3"}}}
	base, ok := keepPeerURLs(&out, "https://b.example.org")
	if !ok || base != "https://b.example.org/api/v1/stream/T1/" {
		t.Fatalf("%q %v", base, ok)
	}
	relayURLs(&out, base, "LOCAL")
	if out.Stream.URL != "/api/v1/stream/LOCAL/master.m3u8" || out.SubtitleTracks[0].URL != "/api/v1/stream/LOCAL/subtitles/s3" {
		t.Fatalf("%+v", out)
	}
	for _, u := range []string{"https://b.example.org/api/v1/stream/T2/subtitles/s3", "https://b.example.org/api/v1/stream/T1/x/y/z",
		"https://c.example.org/api/v1/stream/T1/subtitles/s3",
		"https://b.example.org/api/v1/stream/T1/..", "https://b.example.org/api/v1/stream/T1/.",
		"https://b.example.org/api/v1/stream/T1/subtitles/..", "https://b.example.org/api/v1/stream/T1/pictures/."} {
		bad := sessionJSON{ID: out.ID, Stream: streamJSON{URL: "https://b.example.org/api/v1/stream/T1/file", Protocol: "file"},
			SubtitleTracks: []trackJSON{{URL: u}}}
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
