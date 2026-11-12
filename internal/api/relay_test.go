package api

import "testing"

// A paired server's playlist passes only when it names files of the same
// stream, by name: no path, no other server (rule 8).
func TestCheckRelayedPlaylist(t *testing.T) {
	for _, tc := range []struct {
		body string
		ok   bool
	}{
		{"#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv0.m3u8\n", true},
		{"#EXTM3U\n#EXT-X-MAP:URI=\"init0.mp4\"\n#EXTINF:6,\ns0.mp4\r\n", true},
		{"#EXTM3U\nhttps://elsewhere.example.org/s0.ts\n", false},
		{"#EXTM3U\n../../api/v1/me\n", false},
		{"#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"https://k.example.org/key\"\n", false},
		{"<html>\n", false},
	} {
		if err := checkRelayedPlaylist([]byte(tc.body)); (err == nil) != tc.ok {
			t.Errorf("%q: %v", tc.body, err)
		}
	}
}

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
		"https://c.example.org/api/v1/stream/T1/subtitles/s3"} {
		bad := sessionJSON{ID: out.ID, Stream: streamJSON{URL: "https://b.example.org/api/v1/stream/T1/file", Protocol: "file"},
			SubtitleTracks: []trackJSON{{URL: u}}}
		if _, ok := keepPeerURLs(&bad, "https://b.example.org"); ok {
			t.Errorf("accepted %s", u)
		}
	}
}
