package api

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// What relayedPlaylist keeps names files of the stream only, has no
// character players end lines at inside a line, holds only the subset's
// tags, and is kept whole when checked again.
func FuzzRelayedPlaylist(f *testing.F) {
	for _, s := range []string{"#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv0.m3u8\n",
		"#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:6,\nseg0.m4s\r\n#EXT-X-ENDLIST\n",
		"#EXTM3U\n#EXT-X-KEY:URI=\"k\"\n", "#EXTM3U\n#EXTINF:6,\rhttps://e.example/s\n", "#EXTM3U\n#X:uri=\"a\"URI=\"b\"\n"} {
		f.Add([]byte(s), false)
		f.Add([]byte(s), true)
	}
	f.Fuzz(func(t *testing.T, body []byte, master bool) {
		subset := mediaSubset
		if master {
			subset = masterSubset
		}
		out, err := relayedPlaylist(body, subset)
		if err != nil {
			return
		}
		if !bytes.HasPrefix(out, []byte("#EXTM3U\n")) {
			t.Fatalf("kept %q", out)
		}
		for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
			if strings.ContainsFunc(line, lineBreaking) || line == "" {
				t.Fatalf("a line %q kept", line)
			}
			if strings.HasPrefix(line, "#") {
				tag, _, _ := strings.Cut(line, ":")
				found := false
				for _, s := range subset {
					found = found || s == tag
				}
				if !found {
					t.Fatalf("a tag outside the subset kept: %q", line)
				}
				continue
			}
			if !fileName(line) {
				t.Fatalf("an address kept: %q", line)
			}
		}
		again, err := relayedPlaylist(out, subset)
		if err != nil || !bytes.Equal(again, out) {
			t.Fatalf("checked again: %q, %v", again, err)
		}
	})
}

// A 206's headers pass checkPartial only for a part of the range asked,
// its length end − start + 1, and Content-Length (when given) that.
func FuzzCheckPartial(f *testing.F) {
	f.Add("bytes=0-9", "bytes 0-9/100", "10")
	f.Add("bytes=-10", "bytes 90-99/100", "")
	f.Add("bytes=5-", "bytes 5-9/*", "5")
	f.Add("bytes=0-1,5-9", "bytes 0-1/10", "2")
	f.Fuzz(func(t *testing.T, rangeHeader, contentRange, contentLength string) {
		br, ok := parseRange(rangeHeader)
		if !ok {
			return
		}
		h := http.Header{}
		h.Set("Content-Range", contentRange)
		if contentLength != "" {
			h.Set("Content-Length", contentLength)
		}
		n, ok := checkPartial(rangeHeader, br, h)
		if !ok {
			return
		}
		m := contentRangeRe.FindStringSubmatch(contentRange)
		if m == nil {
			t.Fatalf("passed %q", contentRange)
		}
		start, _ := strconv.ParseInt(m[1], 10, 64)
		end, _ := strconv.ParseInt(m[2], 10, 64)
		switch {
		case n != end-start+1 || n < 1:
			t.Fatalf("%q: length %d", contentRange, n)
		case contentLength != "" && contentLength != strconv.FormatInt(n, 10):
			t.Fatalf("%q with Content-Length %q", contentRange, contentLength)
		case br.suffix == 0 && (start != br.first || br.last >= 0 && end > br.last):
			t.Fatalf("%q for %q", contentRange, rangeHeader)
		case m[3] != "*":
			if total, _ := strconv.ParseInt(m[3], 10, 64); end >= total || br.suffix > 0 && start != max(total-br.suffix, 0) {
				t.Fatalf("%q for %q", contentRange, rangeHeader)
			}
		case br.suffix > 0:
			t.Fatalf("a suffix range passed with an unknown total: %q", contentRange)
		}
	})
}
