// Package textclean turns untrusted metadata into plain text (SECURITY.md
// rule 9). Metadata comes from internet providers and media files, so it may
// carry markup, control characters or invisible direction overrides. What
// comes out is plain text, safe to show anywhere, and never longer than asked.
package textclean

import (
	"html"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Name tidies a name someone typed (a profile's, a category's…): spaces
// around it go, and runs of them inside become one. ok is false unless
// it's then plain text of at most max characters, accented ones and other
// scripts counted as one each; "" is ok.
func Name(s string, max int) (name string, ok bool) {
	name = strings.Join(strings.Fields(s), " ")
	return name, utf8.RuneCountInString(name) <= max && Plain(name, len(name)) == name
}

// Plain removes markup, control characters and bidirectional overrides,
// collapses runs of whitespace (keeping paragraph breaks), and cuts the
// result to at most max bytes without splitting a character.
func Plain(s string, max int) string {
	// Invisible characters go before tags are looked for: dropped later,
	// "<\u202Escript>" would close up into "<script>".
	s = stripTags(dropInvisible(s))
	s = html.UnescapeString(s) // &amp; becomes &, after the tags are gone
	// Again: "&lt;script&gt;" decodes to a tag, which a careless site
	// rendering text as HTML would run.
	s = stripTags(dropInvisible(s))

	var b strings.Builder
	space, newlines := false, 0
	for _, r := range s {
		switch {
		case r == '\n':
			newlines++
			space = false
			continue
		case isBidiControl(r), r == utf8.RuneError:
			continue
		case unicode.IsControl(r), unicode.IsSpace(r):
			space = true
			continue
		}
		// Write pending whitespace once, now that real text follows.
		if b.Len() > 0 {
			switch {
			case newlines >= 2:
				b.WriteString("\n\n")
			case newlines == 1:
				b.WriteString("\n")
			case space:
				b.WriteByte(' ')
			}
		}
		space, newlines = false, 0
		b.WriteRune(r)
	}
	return truncate(b.String(), max)
}

// stripTags drops markup: from a "<" that starts a tag (followed by a letter,
// "/", "!" or "?") to the next ">". A lone "<", as in "a < b" or "<3", stays.
// The output is plain text, so this only has to remove markup, not
// interpret it.
func stripTags(s string) string {
	var b strings.Builder
	inTag := false
	for i, r := range s {
		switch {
		case inTag:
			if r == '>' {
				inTag = false
				b.WriteByte(' ') // "a<br>b" reads as "a b"
			}
		case r == '<' && startsTag(s[i+1:]):
			inTag = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func startsTag(rest string) bool {
	r, _ := utf8.DecodeRuneInString(rest)
	return unicode.IsLetter(r) || r == '/' || r == '!' || r == '?'
}

// dropInvisible removes direction controls and invalid UTF-8.
func dropInvisible(s string) string {
	return strings.Map(func(r rune) rune {
		if r == utf8.RuneError || isBidiControl(r) {
			return -1
		}
		return r
	}, s)
}

// isBidiControl reports the invisible characters that change text direction
// and can make a title display as something other than what it is.
func isBidiControl(r rune) bool {
	switch {
	case r >= 0x202A && r <= 0x202E, // embeddings and overrides
		r >= 0x2066 && r <= 0x2069,            // isolates
		r == 0x200E, r == 0x200F, r == 0x061C: // marks
		return true
	}
	return false
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimRight(s[:cut], " \n")
}
