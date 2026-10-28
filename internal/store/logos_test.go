package store

import (
	"context"
	"slices"
	"testing"
	"time"
)

// Migration 066 lists every film and series with a logo kept, not seasons
// or episodes; a checked title's logo changes, or goes, at once with its
// place on the list, and only where one was kept.
func TestLogosToCheck(t *testing.T) {
	s := openAt(t, 65)
	ctx := context.Background()
	images := map[string]map[string]string{
		"movie/tmdb/550988":      {"logo": "/enlogo1234.png", "poster": "/poster1234.jpg"},
		"movie/tmdb/550988/s1":   {"logo": "/odd123456.png"},
		"series/tmdb/1399":       {"logo": "/gotlogo123.png"},
		"series/tmdb/1399/s1/e2": {"still": "/still12345.jpg"},
		"series/tmdb/1400":       {"poster": "/poster5678.jpg"}, // no logo
	}
	for _, target := range []ArtworkTarget{{Kind: "movie", TMDB: "550988"}, {Kind: "series", TMDB: "1399"}, {Kind: "series", TMDB: "1400"}} {
		if err := s.SetArtwork(ctx, target, images, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	todo, err := s.LogosToCheck(ctx, 10)
	var keys []string
	for _, t := range todo {
		keys = append(keys, t.Key())
	}
	if err != nil || !slices.Equal(keys, []string{"movie/tmdb/550988", "series/tmdb/1399"}) {
		t.Fatalf("to check: %v %v", keys, err)
	}
	s.SetTitleTexts(ctx, "en-CA", Title{Kind: "movie", TMDB: "550988", Name: "Free Guy"}, []byte(`{}`), "", time.Now())
	if got, _ := s.TextLanguages(ctx, "movie", "550988"); !slices.Equal(got, []string{"en-CA"}) {
		t.Errorf("text languages: %v", got)
	}
	if err := s.SetCheckedLogo(ctx, todo[0], "/frlogo1234.png", map[string]string{"en-CA": "/enlogo1234.png", "de-DE": "/delogo1234.png"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCheckedLogo(ctx, todo[1], "", nil); err != nil {
		t.Fatal(err)
	}
	art, _ := s.Artwork(ctx, []string{"movie/tmdb/550988", "series/tmdb/1399", "series/tmdb/1400"})
	if art["movie/tmdb/550988"]["logo"] != "/frlogo1234.png" || art["movie/tmdb/550988"]["poster"] != "/poster1234.jpg" {
		t.Errorf("film: %v", art["movie/tmdb/550988"])
	}
	if _, ok := art["series/tmdb/1399"]["logo"]; ok {
		t.Errorf("series: %v", art["series/tmdb/1399"])
	}
	if _, ok := art["series/tmdb/1400"]["logo"]; ok {
		t.Errorf("a logo was added: %v", art["series/tmdb/1400"])
	}
	var logo string
	s.db.QueryRowContext(ctx, `SELECT logo FROM catalog_title_texts WHERE kind = 'movie' AND tmdb = '550988' AND language = 'en-CA'`).Scan(&logo)
	if logo != "/enlogo1234.png" {
		t.Errorf("English logo: %q", logo)
	}
	if n, _ := s.TextLanguages(ctx, "movie", "550988"); len(n) != 1 {
		t.Errorf("a language's texts were added: %v", n)
	}
	if todo, err := s.LogosToCheck(ctx, 10); err != nil || len(todo) != 0 {
		t.Errorf("left: %+v %v", todo, err)
	}
}
