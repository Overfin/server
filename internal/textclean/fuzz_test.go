package textclean

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// What comes out is plain text whatever goes in: no markup, no control
// characters but newlines, no direction overrides, valid UTF-8, within
// the length asked, and no stray whitespace at either end.
func FuzzPlain(f *testing.F) {
	for _, s := range []string{
		"Walt and Hank<br/>face off.", "<script>alert(1)</script>Title", "<i>Italic</i> &amp; bold",
		"&lt;script&gt;alert(1)", "a < b and <3", "<!-- hidden -->shown", "Para one.\n\nPara two.",
		"lots   of \t spaces", "evil\u202Egnp.exe", "bell\x07char", "  trimmed  ",
		"é", "<\u202Escript>", "&lt;&#x202E;b&gt;",
	} {
		f.Add(s, uint16(1000))
	}
	f.Fuzz(func(t *testing.T, s string, max uint16) {
		out := Plain(s, int(max))
		if len(out) > int(max) || !utf8.ValidString(out) {
			t.Fatalf("Plain(%q, %d) = %q: too long or not UTF-8", s, max, out)
		}
		if out != strings.TrimSpace(out) {
			t.Fatalf("Plain(%q) = %q: whitespace at an end", s, out)
		}
		for i, r := range out {
			if r == utf8.RuneError || isBidiControl(r) || (unicode.IsControl(r) && r != '\n') {
				t.Fatalf("Plain(%q) = %q: %U kept", s, out, r)
			}
			if r == '<' && startsTag(out[i+1:]) {
				t.Fatalf("Plain(%q) = %q: markup kept", s, out)
			}
		}
	})
}
