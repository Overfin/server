package msfp

import (
	"net/url"
	"strings"
	"unicode"
)

// maxName caps a name, in code points (MSFP core section 9.1).
const maxName = 80

// cleanName cleans a peer's name, or a code's note, as MSFP core section
// 9.1 says, in its order: each run of white space one space, other
// control and format characters removed, both ends trimmed, cut to 80
// code points and trimmed again. An empty name becomes the host of
// address, with its port ("" when address is "").
func cleanName(name, address string) string {
	name = strings.Join(strings.FieldsFunc(name, func(r rune) bool { return unicode.Is(unicode.White_Space, r) }), " ")
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if r := []rune(name); len(r) > maxName {
		name = strings.TrimSpace(string(r[:maxName]))
	}
	if name == "" && address != "" {
		if u, err := url.Parse(address); err == nil {
			name = u.Host
		}
	}
	return name
}
