package playback

import (
	"strings"
	"testing"

	"github.com/overfin/server/internal/jellyfin"
)

// Whatever Jellyfin's playlists hold, what reaches clients names only
// Overfin's neutral files, each mapped to an allowed route: no address, no
// key, no way out of the playback's own folder.
func FuzzRewrite(f *testing.F) {
	f.Add(master)
	f.Add(variant)
	f.Add("#EXTM3U\n../../../Users/x/main.m3u8\n")
	f.Fuzz(func(t *testing.T, body string) {
		for _, rw := range []func([]byte, Upstream) (*Rewritten, error){
			func(b []byte, _ Upstream) (*Rewritten, error) { return RewriteMaster(b, masterUp) },
			func(b []byte, _ Upstream) (*Rewritten, error) { return RewriteVariant(b, variantUp) },
		} {
			r, err := rw([]byte(body), Upstream{})
			if err != nil {
				continue
			}
			for _, line := range strings.Split(string(r.Body), "\n") {
				if line == "" {
					continue
				}
				if strings.Contains(line, "://") {
					t.Fatalf("an address: %q", line)
				}
				if !strings.HasPrefix(line, "#") {
					if _, ok := r.Files[line]; !ok {
						t.Fatalf("a line that isn't a file of ours: %q", line)
					}
				}
			}
			for name, up := range r.Files {
				if strings.ContainsAny(name, "/?#\\") || !allowedRoute.MatchString(up.Path) || strings.Contains(up.Path, "..") {
					t.Fatalf("file %q -> %+v", name, up)
				}
			}
		}
	})
}

// Jellyfin's stream URL is taken only as a relative path on a streaming
// route of the item asked for (rule 10), whatever it holds.
func FuzzDecide(f *testing.F) {
	f.Add("/videos/" + item + "/master.m3u8?MediaSourceId=" + source + "&ApiKey=secret-token")
	f.Add("http://evil.example/videos/" + item + "/master.m3u8")
	f.Add("/videos/" + item + "/master.m3u8?a=1#x")
	f.Add("/videos/../videos/" + item + "/master.m3u8")
	f.Add("/videos/%2e%2e/" + item + "/master.m3u8")
	f.Fuzz(func(t *testing.T, transcodingURL string) {
		d, err := Decide(info(jellyfin.MediaSource{TranscodingUrl: transcodingURL, TranscodingSubProtocol: "hls"}), item, false, "dev")
		if err != nil {
			return
		}
		m := transcodePath.FindStringSubmatch(d.Upstream.Path)
		if m == nil {
			t.Fatalf("%q -> %+v", transcodingURL, d.Upstream)
		}
		if id, _ := jellyfin.NormalizeID(m[2]); id != item || strings.Contains(d.Upstream.Path, "..") || d.Method == "direct" {
			t.Fatalf("%q -> %+v", transcodingURL, d.Upstream)
		}
	})
}
