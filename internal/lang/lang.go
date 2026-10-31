// Package lang tells whether two language names are the same language,
// however they're written: the site's ("fr", "fr-CA") or a media file's
// (ISO 639-2, "fre" or "fra").
package lang

import "strings"

// Base is a language's two-letter code (ISO 639-1), from a two-letter
// code, a BCP 47 tag or a three-letter one; "" when it's none of the
// languages known here.
func Base(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(tag, "-_"); i >= 0 {
		tag = tag[:i]
	}
	switch len(tag) {
	case 2:
		if _, ok := byTwo[tag]; ok {
			return tag
		}
	case 3:
		return threeToTwo[tag]
	}
	return ""
}

// Same says whether a and b are the same language; never for unknown ones.
func Same(a, b string) bool {
	x := Base(a)
	return x != "" && x == Base(b)
}
