package api

import (
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/overfin/server/internal/tmdb"
)

// Where people are sent back after signing in stays on this site.
func FuzzSafeReturn(f *testing.F) {
	for _, s := range []string{"/", "/title/x?y=1", "//evil.example", "/\\evil.example", "https://evil.example", "/%2F%2Fevil", "/\t/evil"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := safeReturn(s)
		if out == "/" {
			return
		}
		u, err := url.Parse(out)
		if out != s || err != nil || u.Host != "" || u.Scheme != "" || !strings.HasPrefix(out, "/") ||
			strings.HasPrefix(out, "//") || strings.ContainsAny(out, "\\\t\r\n") {
			t.Fatalf("%q -> %q", s, out)
		}
	})
}

// An email accepted is one plain address, nothing an email header could
// be split with.
func FuzzCleanEmail(f *testing.F) {
	for _, s := range []string{"ann@example.org", " ann@example.org ", "Ann <ann@example.org>", "\"a\\\"b\"@example.org", "a@b", "a@b.c\r\nBcc: x@y.z"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out, ok := cleanEmail(s)
		if !ok {
			return
		}
		if strings.ContainsAny(out, "\r\n\x00<>,;") || strings.Count(out, "@") < 1 || len(out) > 254 {
			t.Fatalf("%q -> %q", s, out)
		}
	})
}

// Title IDs from clients never break the parser, and those it takes come
// back the same: one title has one ID, and a TMDB ID is TMDB's number as
// TMDB writes it.
func FuzzTitleRef(f *testing.F) {
	for _, s := range []string{"title_movie_tmdb_949", "title_series_tmdb_1399_s2_e3", "title_item_0123456789abcdef0123456789abcdef", "title_series_tmdb_1_s-1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		ref, ok := titleRef(s)
		if !ok {
			return
		}
		if titleID(ref.Key()) != s {
			t.Fatalf("%q -> %q", s, titleID(ref.Key()))
		}
		if id := ref.TMDB(); id != "" {
			if n, err := strconv.Atoi(id); err != nil || strconv.Itoa(n) != id || !tmdb.ValidID(n) {
				t.Fatalf("%q -> TMDB %q", s, id)
			}
		}
	})
}

// A listing's query is taken only with every value in range, and the
// index reads the same query the same way.
func FuzzItemQuery(f *testing.F) {
	f.Add("kind=movie&kind=series&genre=Drama&yearFrom=1990&yearTo=2000&sort=random&seed=abc&limit=20")
	f.Add("library=lib_0123456789abcdef0123456789abcdef&played=true&order=desc&sort=lastPlayed")
	f.Add("kind=movie&yearFrom=1799&genre=a|b&limit=0")
	f.Add("parent=item_0123456789abcdef0123456789abcdef&person=person_0123456789abcdef0123456789abcdef&studio=A24")
	f.Fuzz(func(t *testing.T, raw string) {
		q, err := url.ParseQuery(raw)
		if err != nil {
			return
		}
		if n, ok := parseLimit(q.Get("limit")); ok && (n < 1 || n > 200) {
			t.Fatalf("limit %d", n)
		}
		jq, ok := parseItemQuery(q)
		if !ok {
			return
		}
		if len(jq.Genres) > 10 || len(jq.Types) > 10*4 || (jq.ParentID != "" && len(jq.ParentID) != 32) {
			t.Fatalf("%q -> %+v", raw, jq)
		}
		for _, g := range append(jq.Genres, jq.Studios...) {
			if g == "" || utf8.RuneCountInString(g) > 128 || strings.ContainsAny(g, "|\x00\r\n") {
				t.Fatalf("%q -> name %q", raw, g)
			}
		}
		for _, d := range []string{jq.MinDate, jq.MaxDate} {
			if d != "" && (len(d) != 10 || d < "1800" || d > "9999-12-31") {
				t.Fatalf("%q -> date %q", raw, d)
			}
		}
		lq, ok := indexQuery(q, true)
		if !ok {
			return
		}
		if (lq.YearFrom != 0 && (lq.YearFrom < 1800 || lq.YearFrom > 9999)) || (lq.YearTo != 0 && (lq.YearTo < 1800 || lq.YearTo > 9999)) ||
			len(lq.Genres) != len(jq.Genres) {
			t.Fatalf("%q -> %+v", raw, lq)
		}
	})
}

// The language picked from a browser's is one offered, or the default.
func FuzzPickLanguage(f *testing.F) {
	f.Add("fr-CA,fr;q=0.9,en;q=0.8", "fr-FR,en-US", "en-US")
	f.Add("", "en-US", "en-US")
	f.Add("*,;,,", "", "fr-FR")
	f.Fuzz(func(t *testing.T, accept, offered, def string) {
		list := strings.Split(offered, ",")
		got := pickLanguage(accept, list, def)
		if got != def && !slices.Contains(list, got) {
			t.Fatalf("%q from %q -> %q", accept, list, got)
		}
	})
}
