package identify

import (
	"testing"
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
