package identify

import (
	"testing"
	"unicode/utf8"
)

// Names as folders and files carry them: IDs of every kind, scene names
// with or without a year, and names that look like release tags but
// aren't.
func TestParseNames(t *testing.T) {
	for in, want := range map[string]named{
		"Spirited Away (2001) [imdbid-tt0245429]":     {name: "Spirited Away", year: 2001, imdb: "tt0245429"},
		"Dark (2017) [tvdbid-334824]":                 {name: "Dark", year: 2017, tvdb: "334824"},
		"Heat (1995) {imdb-TT0113277} [tmdbid-949]":   {name: "Heat", year: 1995, tmdb: "949", imdb: "tt0113277"},
		"Le Parrain [tt0068646]":                      {name: "Le Parrain", imdb: "tt0068646"},
		"The.Candidate.1080p.WEB-DL.x264-GROUP.mkv":   {name: "The Candidate"},
		"Some.Show.2160p.UHD.BluRay.mkv":              {name: "Some Show"},
		"Dune.Part.Two.2024.MULTi.2160p.WEB.H265-ABC": {name: "Dune Part Two", year: 2024},
		"The French Connection (1971)":                {name: "The French Connection", year: 1971},
		"The.French.Connection.REMUX.mkv":             {name: "The French Connection"},
		"Web (2013)":                                  {name: "Web", year: 2013},
		"Multi":                                       {name: "Multi"},
		"Mission-Impossible":                          {name: "Mission-Impossible"},
		"[tvdbid-0]":                                  {},
	} {
		if got := parse(in); got != want {
			t.Errorf("%q: %+v, want %+v", in, got, want)
		}
	}
}

// Episodes' numbers: by season (several ways of writing it, several
// episodes in one file), or across the whole series; groups, resolutions
// and checksums in brackets are never a number.
func TestReadEpisode(t *testing.T) {
	for in, want := range map[string]episodeNumbers{
		"Breaking Bad S01E02.mkv":    {season: 1, episode: 2},
		"breaking.bad.s1e2.720p.mkv": {season: 1, episode: 2},
		"Show - S02E03E04.mkv":       {season: 2, episode: 3, last: 4},
		"Show - S02E03-E04.mkv":      {season: 2, episode: 3, last: 4},
		"Show S10E100.mkv":           {season: 10, episode: 100},
		"Show 3x07.mkv":              {season: 3, episode: 7},
		"One Piece - 1071.mkv":       {absolute: 1071},
		"[SubsPlease] One Piece - 1071 (1080p) [ABCD1234].mkv": {absolute: 1071},
		"Naruto Shippuden - E045v2.mkv":                        {absolute: 45},
		"Bleach Ep 12.mkv":                                     {absolute: 12},
		"Show #7.mkv":                                          {absolute: 7},
	} {
		got, ok := readEpisode(in)
		if !ok || got != want {
			t.Errorf("%q: %+v %v, want %+v", in, got, ok, want)
		}
	}
	for _, in := range []string{"Movie (1080p).mkv", "[1080p] Show.mkv", "Heat (1995).mkv", "1917.mkv"} {
		if got, ok := readEpisode(in); ok {
			t.Errorf("%q: %+v, want none", in, got)
		}
	}
}

// Any file name gives numbers in range, or none.
func FuzzReadEpisode(f *testing.F) {
	for _, s := range []string{"Show S01E02.mkv", "One Piece - 1071.mkv", "Show 3x07", "S9999E9999E9999", "#0", " - "} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		e, ok := readEpisode(s)
		if !ok {
			if e != (episodeNumbers{}) {
				t.Fatalf("%q: %+v without numbers", s, e)
			}
			return
		}
		if e.season < 0 || e.season > 9999 || e.episode < 0 || e.episode > 9999 || e.absolute < 0 || e.absolute > 9999 ||
			(e.last != 0 && e.last <= e.episode) || (e.absolute == 0 && e.episode == 0 && e.season == 0) {
			t.Fatalf("%q: %+v", s, e)
		}
		if utf8.ValidString(s) && e.absolute > 0 && e.episode > 0 {
			t.Fatalf("%q: both kinds: %+v", s, e)
		}
	})
}
