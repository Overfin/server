package lang

import "testing"

func TestSame(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"fr", "fre", true}, {"fr-CA", "fra", true}, {"en", "eng", true}, {"FR", "fr", true}, {"de", "ger", true},
		{"fr", "eng", false}, {"und", "und", false}, {"", "", false}, {"xx", "xx", false}, {"pt-BR", "por", true},
	} {
		if got := Same(c.a, c.b); got != c.same {
			t.Errorf("%q %q: %v", c.a, c.b, got)
		}
	}
	if Base("fr_CA") != "fr" || Base("zzz") != "" {
		t.Error("base")
	}
}

// Tags read however they come; names each in itself, with the country of a
// variant an operator can offer, the code of a language unknown here, "?"
// for none.
func TestNames(t *testing.T) {
	for in, want := range map[string]string{
		"fr": "fr", "fre": "fr", "FR_ca": "fr-CA", "fr-CA": "fr-CA", "es-419": "es-419", "pt-br-x": "pt-BR",
		"zz": "", "und": "", "": "", "fr-Latn": "fr",
	} {
		if got := Tag(in); got != want {
			t.Errorf("Tag(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"fr-CA": "Français (Canada)", "fra": "Français", "fr-BE": "Français", "es-MX": "Español (México)",
		"es-419": "Español (Latinoamérica)", "es-AR": "Español", "jpn": "日本語", "hi": "हिन्दी", "cn": "粵語", "yue": "粵語", "tl": "Tagalog", "fil": "Tagalog",
		"xx": "xx", "qya": "qya", "und": "?", "": "?", "zxx": "?", "fr3": "?", "a long name": "?",
	} {
		if got := Name(in); got != want {
			t.Errorf("Name(%q) = %q, want %q", in, got, want)
		}
	}
	// Every language once, and every offered one known.
	seen := map[string]bool{}
	for _, l := range languages {
		if seen[l.two] || l.name == "" {
			t.Errorf("%s: twice, or nameless", l.two)
		}
		seen[l.two] = true
	}
	for _, c := range Choices {
		if !seen[Base(c.Tag)] {
			t.Errorf("%s is offered, not known", c.Tag)
		}
	}
	if len(languages) < 100 {
		t.Errorf("%d languages", len(languages))
	}
}
