package store

import (
	"regexp"
	"testing"
)

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

// The patterns title keys were first checked with, kept to check the
// parser against: it must accept exactly what they accept, and read the
// same parts.
var (
	titleKeyPattern = regexp.MustCompile(`^(movie|series)/(tmdb|imdb|tvdb)/([A-Za-z0-9]{1,20})(?:/s([0-9]{1,4})(?:/e([0-9]{1,5}))?)?$`)
	itemKeyPattern  = regexp.MustCompile(`^item/([0-9a-f]{32})$`)
)

func parseTitleKeyByPattern(key string) (TitleRef, bool) {
	if m := itemKeyPattern.FindStringSubmatch(key); m != nil {
		return TitleRef{Kind: "item", ID: m[1]}, true
	}
	m := titleKeyPattern.FindStringSubmatch(key)
	if m == nil || (m[1] == "movie" && m[4] != "") {
		return TitleRef{}, false
	}
	r := TitleRef{Kind: m[1], Source: m[2], ID: m[3]}
	if m[4] != "" {
		r.Kind, r.Season = "season", atoi(m[4])
	}
	if m[5] != "" {
		r.Kind, r.Episode = "episode", atoi(m[5])
	}
	return r, true
}

func FuzzParseTitleKeyAsPattern(f *testing.F) {
	for _, s := range []string{"movie/tmdb/949", "series/tmdb/1399", "series/tmdb/1399/s1", "series/tmdb/1399/s0/e12",
		"series/tvdb/81189/s2/e3", "movie/imdb/tt0111161", "item/0123456789abcdef0123456789abcdef", "movie/tmdb/949/s1",
		"series/tmdb/1/s01/e002", "item/0123456789ABCDEF0123456789abcdef", "item/0123456789abcdef0123456789abcde",
		"series/tmdb/1/s12345", "series/tmdb/1/s1/e123456", "series/tmdb/123456789012345678901", "series/tmdb//s1",
		"series/tmdb/1/s/e1", "series/tmdb/1/s1/e", "series/tmdb/1/s1/e1/", "series/tmdb/1/", "movie/tmdb/1\\n",
		"movie/tmdb/é", "series/tmdb/1/x1", "series/tmdb/1/s1/x1", "show/tmdb/1", "movie/tvdb/1", "movie/tmdb/-1",
		"series/tmdb/1/s+1", "series/tmdb/1/s1/e1/e2", "item/", "item/0123456789abcdef0123456789abcdef/s1", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, ok := ParseTitleKey(s)
		want, wantOK := parseTitleKeyByPattern(s)
		if ok != wantOK || got != want {
			t.Fatalf("%q: %+v %v, want %+v %v", s, got, ok, want, wantOK)
		}
	})
}
