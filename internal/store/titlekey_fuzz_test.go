package store

import "testing"

// A title key that parses gives a ref whose own key parses back to the
// same ref, with seasons and episodes in range.
func FuzzParseTitleKey(f *testing.F) {
	for _, s := range []string{"movie/tmdb/949", "series/tmdb/1399", "series/tmdb/1399/s1", "series/tmdb/1399/s0/e12",
		"series/tvdb/81189/s2/e3", "movie/imdb/tt0111161", "item/0123456789abcdef0123456789abcdef", "movie/tmdb/949/s1",
		"series/tmdb/1/s01/e002"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		r, ok := ParseTitleKey(s)
		if !ok {
			return
		}
		back, ok := ParseTitleKey(r.Key())
		if !ok || back != r {
			t.Fatalf("%q -> %+v -> %q -> %+v %v", s, r, r.Key(), back, ok)
		}
		if r.Season < 0 || r.Season > 9999 || r.Episode < 0 || r.Episode > 99999 || r.ID == "" {
			t.Fatalf("%q -> %+v", s, r)
		}
		switch r.Kind {
		case "movie", "series", "item":
			if r.Season != 0 || r.Episode != 0 {
				t.Fatalf("%q -> %+v", s, r)
			}
		case "season":
			if r.Episode != 0 {
				t.Fatalf("%q -> %+v", s, r)
			}
		case "episode":
		default:
			t.Fatalf("%q -> kind %q", s, r.Kind)
		}
	})
}
