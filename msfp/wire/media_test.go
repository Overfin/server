package wire

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// A paired server's playlist passes only when it names files of the same
// stream, by name: no path, no other server.
func TestCheckPlaylist(t *testing.T) {
	for _, tc := range []struct {
		body string
		ok   bool
	}{
		{"#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv0.m3u8\n", true},
		{"#EXTM3U\n#EXT-X-MAP:URI=\"init0.mp4\"\n#EXTINF:6,\ns0.mp4\r\n", true},
		{"#EXTM3U\nhttps://elsewhere.example.org/s0.ts\n", false},
		{"#EXTM3U\n../../api/v1/me\n", false},
		{"#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"https://k.example.org/key\"\n", false},
		// Unquoted, which players may read too.
		{"#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=https://k.example.org/key\n", false},
		{"#EXTM3U\n#EXT-X-MAP:uri=https://k.example.org/i.mp4\n", false},
		{"#EXTM3U\n#EXT-X-MAP:URI=init0.mp4\n", false},
		{"<html>\n", false},
		// "." and "..", which a URL resolves as folders (MSFP media).
		{"#EXTM3U\n..\n", false},
		{"#EXTM3U\n.\n", false},
		{"#EXTM3U\n#EXT-X-MAP:URI=\"..\"\n", false},
		{"#EXTM3U\n...\n", true},
	} {
		if err := CheckPlaylist([]byte(tc.body)); (err == nil) != tc.ok {
			t.Errorf("%q: %v", tc.body, err)
		}
	}
}

// MSFP's media test vectors (https://github.com/msfp-io/msfp-spec,
// vectors/v1/media.json), copied in testdata/v1/media.json: each
// case through the code that checks what a paired server sends.
type mediaVectors struct {
	WebVTT []struct {
		Name   string `json:"name"`
		Text   string `json:"text"`
		Hex    string `json:"hex"`
		Valid  bool   `json:"valid"`
		Output string `json:"output"`
	} `json:"webvtt"`
	HLS []struct {
		Name  string `json:"name"`
		Text  string `json:"text"`
		Valid bool   `json:"valid"`
	} `json:"hls"`
	PGS []struct {
		Name  string `json:"name"`
		Hex   string `json:"hex"`
		Valid bool   `json:"valid"`
	} `json:"pgs"`
}

func readMediaVectors(t *testing.T) mediaVectors {
	t.Helper()
	b, err := os.ReadFile("testdata/v1/media.json")
	if err != nil {
		t.Fatal(err)
	}
	var v mediaVectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestHLSVectors(t *testing.T) {
	v := readMediaVectors(t)
	if len(v.HLS) != 25 {
		t.Fatalf("%d HLS cases", len(v.HLS))
	}
	for _, c := range v.HLS {
		if err := CheckPlaylist([]byte(c.Text)); (err == nil) != c.Valid {
			t.Errorf("%s: %v, want valid %v", c.Name, err, c.Valid)
		}
	}
}

func TestWebVTTVectors(t *testing.T) {
	v := readMediaVectors(t)
	if len(v.WebVTT) == 0 {
		t.Fatal("no WebVTT cases")
	}
	for _, c := range v.WebVTT {
		in := []byte(c.Text)
		if c.Hex != "" {
			in, _ = hex.DecodeString(c.Hex)
		}
		out, ok := RebuildWebVTT(in)
		if ok != c.Valid || (ok && string(out) != c.Output) {
			t.Errorf("%s: %v %q, want %v %q", c.Name, ok, out, c.Valid, c.Output)
		}
	}
}

func TestPGSVectors(t *testing.T) {
	for _, c := range readMediaVectors(t).PGS {
		in, _ := hex.DecodeString(c.Hex)
		if err := CheckPGS(in); (err == nil) != c.Valid {
			t.Errorf("%s: %v, want valid %v", c.Name, err, c.Valid)
		}
	}
}

// A relayed playlist keeps the subset's tags only: comments and other
// tags are dropped, URIs checked in them all the same.
func TestFilterPlaylistKeepsTheSubset(t *testing.T) {
	got, err := FilterPlaylist([]byte("#EXTM3U\r\n# a comment\n#EXT-X-FOO:BAR=1\n#EXT-X-TARGETDURATION:6\n\n#EXTINF:6,\nseg0.ts\n#EXT-X-ENDLIST"), MediaSubset)
	if want := "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6,\nseg0.ts\n#EXT-X-ENDLIST\n"; err != nil || string(got) != want {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := FilterPlaylist([]byte("#EXTM3U\n#EXT-X-FOO:URI=\"a/b\"\n"), MediaSubset); err == nil {
		t.Fatal("a dropped tag's URI still counts")
	}
	if _, err := FilterPlaylist([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv0.m3u8\n"+strings.Repeat("s.ts\n", MaxPlaylistURIs)), nil); err == nil {
		t.Fatal("too many URIs")
	}
	// Byte offsets stay right past letters whose upper case is longer.
	if _, err := FilterPlaylist([]byte("#EXTM3U\n#EXTINF:6,ßßßß uri=\"a/b\"\nseg0.ts\n"), nil); err == nil {
		t.Fatal("a URI after non-ASCII text")
	}
}

// What Overfin as the owning server sent of Jellyfin 12.2's HLS, as a
// paired server's relay read it in a browser check (2026-11-16): within
// the subset, kept whole.
func TestFilterPlaylistOfJellyfin(t *testing.T) {
	master := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=5198950,AVERAGE-BANDWIDTH=5198950,VIDEO-RANGE=SDR,CODECS=\"avc1.64001F,mp4a.40.2\",RESOLUTION=1280x720,FRAME-RATE=24\nv0.m3u8\n"
	media := "#EXTM3U\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:11\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-MAP:URI=\"init.mp4\"\n" +
		"#EXTINF:10.417000,\nseg0.mp4\n#EXTINF:10.416000,\nseg1.mp4\n#EXT-X-ENDLIST\n"
	for body, subset := range map[string][]string{master: MasterSubset, media: MediaSubset} {
		if got, err := FilterPlaylist([]byte(body), subset); err != nil || string(got) != body {
			t.Errorf("%v\n%s", err, got)
		}
	}
}

// A character players read as a line's end, inside a line, would hide
// an address from the check.
func TestFilterPlaylistHiddenLines(t *testing.T) {
	for _, tc := range []struct {
		body   string
		subset []string
	}{
		{"#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\rhttps://elsewhere.example/v.m3u8\n", MasterSubset},
		{"#EXTM3U\n#EXTINF:6, https://elsewhere.example/s.ts\n", MediaSubset},
		{"#EXTM3U\n#EXTINF:6,\rhttps://elsewhere.example/s.ts\n", MediaSubset},
		{"#EXTM3U\n#EXTINF:6,\x0bs.ts\n", MediaSubset},
		{"#EXTM3U\n#EXTINF:6,\u0085s.ts\n", nil},
	} {
		if _, err := FilterPlaylist([]byte(tc.body), tc.subset); err == nil {
			t.Errorf("%q passed", tc.body)
		}
	}
	for _, ok := range []string{"#EXTM3U\r\n#EXT-X-STREAM-INF:BANDWIDTH=1,\tRESOLUTION=1x1\r\nv0.m3u8\r\n"} {
		if _, err := FilterPlaylist([]byte(ok), MasterSubset); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}
