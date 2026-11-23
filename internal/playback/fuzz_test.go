package playback

import (
	"strings"
	"testing"
)

// Whatever Jellyfin's playlists hold, what reaches clients names only
// Overfin's neutral files, each mapped to an allowed route: no address, no
// key, no way out of the playback's own folder.
func FuzzRewrite(f *testing.F) {
	f.Add(variant)
	f.Add("#EXTM3U\n../../../Users/x/main.m3u8\n")
	f.Fuzz(func(t *testing.T, body string) {
		{
			r, err := RewriteVariant([]byte(body), variantUp)
			if err != nil {
				return
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
