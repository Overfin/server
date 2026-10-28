package ids

import (
	"strings"
	"testing"
)

// A valid ID is the prefix, an underscore and 26 characters of the
// alphabet: nothing a URL, a log line or SQL could be bent with.
func FuzzValid(f *testing.F) {
	f.Add(Profile, New(Profile))
	f.Add(Account, New(Profile))
	f.Add("cat", "cat_aaaaaaaaaaaaaaaaaaaaaaaaaa")
	f.Add("cat", "cat_AAAAAAAAAAAAAAAAAAAAAAAAAA")
	f.Fuzz(func(t *testing.T, prefix, s string) {
		if !Valid(prefix, New(prefix)) {
			t.Fatalf("New(%q) not valid", prefix)
		}
		if !Valid(prefix, s) {
			return
		}
		rest, ok := strings.CutPrefix(s, prefix+"_")
		if !ok || len(rest) != 26 || strings.Trim(rest, "abcdefghijklmnopqrstuvwxyz234567") != "" {
			t.Fatalf("Valid(%q, %q)", prefix, s)
		}
	})
}
