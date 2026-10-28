package textclean

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPlain(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Walt and Hank<br/>face off.", "Walt and Hank face off."},
		{"<script>alert(1)</script>Title", "alert(1) Title"},
		{"<i>Italic</i> &amp; bold", "Italic & bold"},
		{"&lt;script&gt;alert(1)", "alert(1)"}, // decoded markup is stripped too
		{"a < b and <3", "a < b and <3"},       // a lone < is just text
		{"<!-- hidden -->shown", "shown"},
		{"Para one.\n\nPara two.", "Para one.\n\nPara two."},
		{"lots   of \t spaces", "lots of spaces"},
		{"evil\u202Egnp.exe", "evilgnp.exe"},
		{"bell\x07char", "bell char"},
		{"  trimmed  ", "trimmed"},
	}
	for _, tt := range tests {
		if got := Plain(tt.in, 1000); got != tt.want {
			t.Errorf("Plain(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPlainLength(t *testing.T) {
	got := Plain(strings.Repeat("é", 100), 51)
	if len(got) > 51 || !utf8.ValidString(got) {
		t.Errorf("got %d bytes, valid UTF-8: %v", len(got), utf8.ValidString(got))
	}
}

// Names are tidied and counted in characters.
func TestName(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"  Bob   Smith ", "Bob Smith", true},
		{strings.Repeat("é", 10), strings.Repeat("é", 10), true},
		{strings.Repeat("é", 11), strings.Repeat("é", 11), false},
		{"<b>x</b>", "<b>x</b>", false},
		{"", "", true},
	} {
		if got, ok := Name(tc.in, 10); got != tc.want || ok != tc.ok {
			t.Errorf("Name(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
