package overfin

import (
	"testing"

	"github.com/overfin/server/internal/tmdb"
)

// Where TMDB's data comes from: without TMDB itself, nothing (a real nil,
// which callers test), the certifications from nowhere either; with it,
// TMDB; a language's texts and logos from a copy in that language, the
// shared client's own language unchanged.
func TestSources(t *testing.T) {
	g := &gateway{sh: &shared{}}
	if g.titleSource() != nil || g.pickSource() != nil || g.collectionSource() != nil || g.episodeIMDbSource() != nil ||
		g.textSource("fr") != nil || g.logoSource("fr") != nil || g.ratingSource() != nil || g.tmdbSource() != nil {
		t.Fatal("a source without TMDB")
	}
	c := &tmdb.Client{Language: func() string { return "en" }}
	g.sh.tmdb = c
	if g.titleSource() != c || g.pickSource() != c || g.collectionSource() != c || g.episodeIMDbSource() != c || g.ratingSource() == nil {
		t.Error("not TMDB")
	}
	fr, ok := g.textSource("fr").(*tmdb.Client)
	if !ok || fr == c || fr.Language() != "fr" || c.Language() != "en" {
		t.Errorf("a language's copy: %v %v", ok, c.Language())
	}
	if de, ok := g.logoSource("de").(*tmdb.Client); !ok || de.Language() != "de" || c.Language() != "en" {
		t.Error("logos' copy")
	}
}
