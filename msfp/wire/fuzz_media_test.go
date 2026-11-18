package wire

import (
	"bytes"
	"strings"
	"testing"
)

// What FilterPlaylist keeps names files of the stream only, has no
// character players end lines at inside a line, holds only the subset's
// tags, and is kept whole when checked again.
func FuzzFilterPlaylist(f *testing.F) {
	for _, s := range []string{"#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv0.m3u8\n",
		"#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:6,\nseg0.m4s\r\n#EXT-X-ENDLIST\n",
		"#EXTM3U\n#EXT-X-KEY:URI=\"k\"\n", "#EXTM3U\n#EXTINF:6,\rhttps://e.example/s\n", "#EXTM3U\n#X:uri=\"a\"URI=\"b\"\n"} {
		f.Add([]byte(s), false)
		f.Add([]byte(s), true)
	}
	f.Fuzz(func(t *testing.T, body []byte, master bool) {
		subset := MediaSubset
		if master {
			subset = MasterSubset
		}
		out, err := FilterPlaylist(body, subset)
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
			if !FileName(line) {
				t.Fatalf("an address kept: %q", line)
			}
		}
		again, err := FilterPlaylist(out, subset)
		if err != nil || !bytes.Equal(again, out) {
			t.Fatalf("checked again: %q, %v", again, err)
		}
	})
}

// A rebuilt WebVTT part is rebuilt again as it is: nothing in it is
// read differently a second time (video section 6.3).
func FuzzRebuildWebVTT(f *testing.F) {
	f.Add([]byte("WEBVTT\n\n00:01.000 --> 00:02.000\n<b>Hi</b> &lt;i&gt;\n"))
	f.Add([]byte("WEBVTT\r\n\r\n1\r\n00:00:01.000 --> 00:00:02.000 align:start\r\na < b &amp; c\r\n"))
	f.Fuzz(func(t *testing.T, in []byte) {
		out, ok := RebuildWebVTT(in)
		if !ok {
			return
		}
		again, ok := RebuildWebVTT(out)
		if !ok || string(again) != string(out) {
			t.Fatalf("%q rebuilt as %q, then %q", in, out, again)
		}
	})
}
