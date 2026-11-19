package api

import "testing"

// The search box's words read as an ID, or not: the forms taken, links to
// the three sites, and what looks like an ID without being one.
func TestParseID(t *testing.T) {
	for _, c := range []struct {
		in   string
		want string // as shown back; "" for words, "x:" for no valid ID
	}{
		{"imdb:tt0133093", "imdb:tt0133093"},
		{"  IMDB: TT0133093 ", "imdb:tt0133093"},
		{"imdb:0133093", "imdb:tt0133093"},
		{"tt0133093", "imdb:tt0133093"},
		{"tt12345678", "imdb:tt12345678"},
		{"https://www.imdb.com/title/tt0133093/?ref_=nv_sr_srsg_0", "imdb:tt0133093"},
		{"imdb.com/fr/title/tt0133093", "imdb:tt0133093"},
		{"https://m.imdb.com/title/tt1480055/", "imdb:tt1480055"},
		{"tmdb:movie/603", "tmdb:movie/603"},
		{"tmdb:tv/1399", "tmdb:tv/1399"},
		{"tmdb:series/1399", "tmdb:tv/1399"},
		{"tmdb:m603", "tmdb:movie/603"},
		{"tmdb:s1399", "tmdb:tv/1399"},
		{"tmdb:e63056", "tmdb:e63056"},
		{"tmdb:603", "tmdb:603"},
		{"tmdb:0603", "tmdb:603"},
		{"tmdb:tv/1399/s1/e2", "tmdb:tv/1399/s1/e2"},
		{"tmdb:tv/1399/season/0/episode/3", "tmdb:tv/1399/s0/e3"},
		{"https://www.themoviedb.org/movie/603-the-matrix", "tmdb:movie/603"},
		{"themoviedb.org/tv/1399-game-of-thrones?language=fr", "tmdb:tv/1399"},
		{"https://www.themoviedb.org/fr/tv/1399", "tmdb:tv/1399"},
		{"https://www.themoviedb.org/tv/1399-game-of-thrones/season/1", "tmdb:tv/1399"},
		{"https://www.themoviedb.org/tv/1399-game-of-thrones/season/1/episode/1", "tmdb:tv/1399/s1/e1"},
		{"tvdb:81189", "tvdb:81189"},
		{"tvdb:series/81189", "tvdb:series/81189"},
		{"https://thetvdb.com/dereferrer/series/81189", "tvdb:series/81189"},
		{"https://thetvdb.com/series/breaking-bad/episodes/349232", "tvdb:episode/349232"},
		{"https://thetvdb.com/?tab=series&id=81189", "tvdb:series/81189"},

		// Looks like an ID, isn't one: nothing is searched.
		{"imdb:", "imdb:"},
		{"imdb:matrix", "imdb:"},
		{"imdb:tt123", "imdb:"},
		{"tt013", "imdb:"},
		{"tt12345678901", "imdb:"},
		{"tmdb:movie/", "tmdb:"},
		{"tmdb:movie/603/s1/e1", "tmdb:"},
		{"tmdb:tv/1399/s1/e0", "tmdb:"},
		{"tmdb:0", "tmdb:"},
		{"tmdb:99999999999", "tmdb:"},
		{"tvdb:breaking-bad", "tvdb:"},
		{"https://www.imdb.com/name/nm0000206/", "imdb:"},
		{"https://www.themoviedb.org/person/6384-keanu-reeves", "tmdb:"},
		{"https://thetvdb.com/series/breaking-bad", "tvdb:"},

		// Words.
		{"matrix", ""},
		{"tt", ""},
		{"ttaaaaaaa", ""},
		{"the matrix imdb", ""},
		{"star trek: picard", ""},
		{"https://example.org/title/tt0133093", ""},
		{"notimdb.com/title/tt0133093", ""},
		{"603", ""},
	} {
		q, ok := parseID(c.in)
		got := ""
		if ok {
			got = q.String()
		}
		if got != c.want {
			t.Errorf("parseID(%q) = %q, want %q", c.in, got, c.want)
		}
		if ok && q.Valid != (c.want != "" && c.want[len(c.want)-1] != ':') {
			t.Errorf("parseID(%q): valid %v", c.in, q.Valid)
		}
	}
}
