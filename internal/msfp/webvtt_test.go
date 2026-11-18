package msfp

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRebuildWebVTT(t *testing.T) {
	in := "\uFEFFWEBVTT\r\n\r\nSTYLE\r\n::cue { background: url(http://198.51.100.1/x) }\r\n\r\nNOTE secret note\r\n\r\n" +
		"1\r\n00:00:01.000 --> 00:00:04.000 position:10% align:start\r\n<i>Hello</i> <script>alert(1)</script>\r\n\r\n" +
		"00:01:00.500 --> 00:01:02.000\r\n<c.yellow>World</c>\r\n"
	out, ok := RebuildWebVTT([]byte(in))
	if !ok {
		t.Fatal("refused a valid file")
	}
	want := "WEBVTT\n\n00:00:01.000 --> 00:00:04.000\nHello alert(1)\n\n00:01:00.500 --> 00:01:02.000\nWorld\n"
	if string(out) != want {
		t.Errorf("got:\n%q\nwant:\n%q", out, want)
	}
	if _, ok := RebuildWebVTT([]byte("<html>")); ok {
		t.Error("accepted a non-WebVTT file")
	}
	// Text that reads as markup in WebVTT is escaped, so it shows as is.
	out, _ = RebuildWebVTT([]byte("WEBVTT\n\n00:01.000 --> 00:02.000\nx < y & z > w, &lt;i&gt;\n"))
	if want := "WEBVTT\n\n00:01.000 --> 00:02.000\nx &lt; y &amp; z &gt; w,\n"; string(out) != want {
		t.Errorf("escaping: %q", out)
	}
}

// Subtitles come out as WebVTT holding cue timings and plain text only.
func FuzzRebuildWebVTTPlain(f *testing.F) {
	f.Add("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n<b>Hi</b>\n")
	f.Add("WEBVTT\n\nSTYLE\n::cue { color: red }\n\n1\n00:01.000 --> 00:02.000 align:start\n<script>x</script>&lt;i&gt;\n")
	f.Fuzz(func(t *testing.T, in string) {
		out, ok := RebuildWebVTT([]byte(in))
		if !ok {
			return
		}
		s := string(out)
		if !strings.HasPrefix(s, "WEBVTT\n") || !utf8.ValidString(s) {
			t.Fatalf("not WebVTT: %q", s)
		}
		for _, line := range strings.Split(s, "\n") {
			if strings.ContainsAny(line, "<\r\x00") && !cueTiming.MatchString(line) {
				t.Fatalf("markup or control characters: %q", line)
			}
			if strings.Contains(strings.NewReplacer("&amp;", "", "&lt;", "", "&gt;", "").Replace(line), "&") {
				t.Fatalf("an & that isn't escaped: %q", line)
			}
			if strings.Contains(line, "-->") && !cueTiming.MatchString(line) {
				t.Fatalf("a cue's text that reads as a timing: %q", line)
			}
		}
	})
}
