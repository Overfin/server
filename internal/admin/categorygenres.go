package admin

import (
	"slices"
	"strings"

	"github.com/overfin/server/internal/store"
)

// The control panel is in English, and TMDB's genres are a short fixed
// list: the panel names them in English whatever language the titles came
// in. The genres only series have say so, as a film genre beside them
// ("Science Fiction" and "Sci-Fi & Fantasy (series)") is a different one.
var genresInEnglish = map[int]string{
	28: "Action", 12: "Adventure", 16: "Animation", 35: "Comedy", 80: "Crime",
	99: "Documentary", 18: "Drama", 10751: "Family", 14: "Fantasy", 36: "History",
	27: "Horror", 10402: "Music", 9648: "Mystery", 10749: "Romance",
	878: "Science Fiction", 10770: "TV Movie", 53: "Thriller", 10752: "War", 37: "Western",
	10759: "Action & Adventure (series)", 10762: "Kids (series)", 10763: "News (series)",
	10764: "Reality (series)", 10765: "Sci-Fi & Fantasy (series)", 10766: "Soap (series)",
	10767: "Talk (series)", 10768: "War & Politics (series)",
}

// panelGenres names genres in English, a genre TMDB added since keeping
// the name it came with, sorted by name.
func panelGenres(genres []store.TasteGenre) []store.TasteGenre {
	out := make([]store.TasteGenre, 0, len(genres))
	for _, g := range genres {
		if name, ok := genresInEnglish[g.ID]; ok {
			g.Name = name
		}
		out = append(out, g)
	}
	slices.SortFunc(out, func(a, b store.TasteGenre) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out
}
