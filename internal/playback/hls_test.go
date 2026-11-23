package playback

import (
	"errors"
	"strings"
	"testing"
)

const vid = "01234567-89ab-cdef-0123-456789abcdef"

var variantUp = Upstream{Path: "/videos/" + vid + "/main.m3u8"}

const variant = `#EXTM3U
#EXT-X-PLAYLIST-TYPE:VOD
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-MAP:URI="hls1/main/-1.mp4?ApiKey=SECRET&runtimeTicks=0"
#EXTINF:6.0, nodesc
hls1/main/0.mp4?ApiKey=SECRET&runtimeTicks=0&actualSegmentLengthTicks=60000000
#EXTINF:6.0, nodesc
hls1/main/1.mp4?ApiKey=SECRET&runtimeTicks=60000000&actualSegmentLengthTicks=60000000
#EXT-X-KEY:METHOD=AES-128,URI="https://198.51.100.1/key"
#EXT-X-ENDLIST
`

func TestRewriteVariant(t *testing.T) {
	r, err := RewriteVariant([]byte(variant), variantUp)
	if err != nil {
		t.Fatal(err)
	}
	out := string(r.Body)
	for _, leak := range []string{"SECRET", "ApiKey", "runtimeTicks", "hls1", "198.51.100.1", "EXT-X-KEY"} {
		if strings.Contains(out, leak) {
			t.Errorf("output contains %q:\n%s", leak, out)
		}
	}
	for _, want := range []string{`#EXT-X-MAP:URI="init.mp4"`, "\nseg0.mp4\n", "\nseg1.mp4\n", "#EXT-X-ENDLIST"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if up := r.Files["seg1.mp4"]; up.Path != "/videos/"+vid+"/hls1/main/1.mp4" || up.Query.Get("runtimeTicks") != "60000000" {
		t.Errorf("upstream for seg1: %+v", up)
	}
	if up := r.Files["init.mp4"]; up.Path != "/videos/"+vid+"/hls1/main/-1.mp4" {
		t.Errorf("upstream for init: %+v", up)
	}
}

func TestRewriteRefuses(t *testing.T) {
	bad := map[string]string{
		"absolute segment":    "#EXTM3U\nhttp://198.51.100.1/hls1/main/0.mp4\n",
		"protocol relative":   "#EXTM3U\n//198.51.100.1/hls1/main/0.mp4\n",
		"traversal":           "#EXTM3U\nhls1/../../System/0.mp4\n",
		"unknown segment":     "#EXTM3U\nhls1/main/0.exe\n",
		"rooted path":         "#EXTM3U\n/System/Info\n",
		"map elsewhere":       "#EXTM3U\n#EXT-X-MAP:URI=\"http://198.51.100.1/x.mp4\"\n",
		"not a playlist":      "<html>hello</html>",
		"segment in a subdir": "#EXTM3U\nhls1/main/deeper/0.mp4\n",
	}
	for name, body := range bad {
		if _, err := RewriteVariant([]byte(body), variantUp); !errors.Is(err, ErrBadPlaylist) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// What Overfin sends stays within MSFP's HLS subset, which a paired
// server checks: a segment's title is dropped, its duration kept, and a
// tag with a uri= in any case is dropped (MSFP media, section 3.1).
func TestRewriteKeepsToTheSubset(t *testing.T) {
	r, err := RewriteVariant([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXT-X-PLAYLIST-TYPE:VOD uri=\"a/b\"\n"+
		"#EXTINF:6.0, uri=\"a/b\"\nhls1/main/0.ts?ApiKey=SECRET\n#EXT-X-ENDLIST\n"), variantUp)
	if err != nil {
		t.Fatal(err)
	}
	if want := "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.0,\nseg0.ts\n#EXT-X-ENDLIST\n"; string(r.Body) != want {
		t.Fatalf("%q", r.Body)
	}
}
