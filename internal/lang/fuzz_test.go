package lang

import "testing"

// Any tag, from a browser, a media file or TMDB, gives a known two-letter
// code or nothing, and a region pick is always one of the choices.
func FuzzBase(f *testing.F) {
	for _, s := range []string{"fr", "fr-CA", "fre", "FRA", " en_US ", "zz", "", "-", "pt-BR-x", "ç"} {
		f.Add(s, "CA")
	}
	f.Fuzz(func(t *testing.T, tag, country string) {
		b := Base(tag)
		if b != "" {
			if _, ok := known[b]; !ok {
				t.Fatalf("Base(%q) = %q", tag, b)
			}
			if Base(b) != b || !Same(tag, b) {
				t.Fatalf("Base(%q) = %q, not the same", tag, b)
			}
		}
		if r := Region(tag, country); r != "" && (!Offered(r) || !Same(r, tag)) {
			t.Fatalf("Region(%q, %q) = %q", tag, country, r)
		}
	})
}
