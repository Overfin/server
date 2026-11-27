package playback

import (
	"errors"
	"strings"
	"testing"
)

// A segment's duration passes alone, as MSFP's check reads it, its title
// never (nor, without a comma, what follows the colon); one that isn't a
// number refuses the playlist.
func TestExtinfDurationOnly(t *testing.T) {
	const head = "#EXTM3U\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-TARGETDURATION:6\n"
	seg := "hls1/main/0.ts\n#EXT-X-ENDLIST"
	for line, want := range map[string]string{
		"#EXTINF:6.000000, nodesc":              "#EXTINF:6.000000,",
		"#EXTINF:6,":                            "#EXTINF:6,",
		"#EXTINF:6":                             "#EXTINF:6,",
		"#EXTINF:4.5,uri=https://x.example.org": "#EXTINF:4.5,",
	} {
		r, err := RewriteVariant([]byte(head+line+"\n"+seg+"\n"), variantUp)
		if err != nil {
			t.Errorf("%q: %v", line, err)
			continue
		}
		if got := strings.Split(string(r.Body), "\n")[3]; got != want {
			t.Errorf("%q became %q, want %q", line, got, want)
		}
	}
	for _, line := range []string{"#EXTINF://", "#EXTINF:https://x.example.org/a", "#EXTINF:", "#EXTINF:-1,", "#EXTINF:1e3,", "#EXTINF:six,"} {
		if _, err := RewriteVariant([]byte(head+line+"\n"+seg+"\n"), variantUp); !errors.Is(err, ErrBadPlaylist) {
			t.Errorf("%q: %v, want refused", line, err)
		}
	}
}
