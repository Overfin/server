package store

import (
	"context"
	"slices"
	"testing"
	"time"
)

// Migration 069 keeps the artwork; a text-free poster is unknown until
// asked (with a title's page, or alone), kept even empty ("TMDB has
// none"), served as a poster; a title whose artwork isn't kept gets none.
func TestTextlessPosters(t *testing.T) {
	ctx := context.Background()
	s := openAt(t, 68)
	now := time.Now()
	film, series := ArtworkTarget{Kind: "movie", TMDB: "550"}, ArtworkTarget{Kind: "series", TMDB: "1399"}
	for _, target := range []ArtworkTarget{film, series} {
		if err := s.SetArtwork(ctx, target, map[string]map[string]string{target.Key(): {"poster": "/frposter12.jpg"}}, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if art, _ := s.Artwork(ctx, []string{film.Key()}); art[film.Key()]["poster"] != "/frposter12.jpg" {
		t.Errorf("artwork kept through the migration: %v", art)
	}
	if known, kept, err := s.TextlessKnown(ctx, film); known || !kept || err != nil {
		t.Errorf("before: known %v, kept %v, %v", known, kept, err)
	}
	if err := s.SetTextless(ctx, film, "/textless123.jpg"); err != nil {
		t.Fatal(err)
	}
	if art, _ := s.Artwork(ctx, []string{film.Key()}); art[film.Key()]["textless"] != "/textless123.jpg" {
		t.Errorf("text-free poster: %v", art)
	}
	if got, _ := s.ImageTitles(ctx, "poster", "/textless123.jpg", 10); !slices.Equal(got, []string{film.Key()}) {
		t.Errorf("served as a poster for: %v", got)
	}
	// TMDB has none: known, not asked again.
	if err := s.SetTextless(ctx, series, ""); err != nil {
		t.Fatal(err)
	}
	if known, _, _ := s.TextlessKnown(ctx, series); !known {
		t.Error("none, unknown")
	}
	// The page fetched again says it too, empty or not.
	if err := s.SetArtwork(ctx, series, map[string]map[string]string{series.Key(): {"poster": "/frposter34.jpg", "textless": ""}}, now); err != nil {
		t.Fatal(err)
	}
	if known, _, _ := s.TextlessKnown(ctx, series); !known {
		t.Error("none from the page, unknown")
	}
	// A title whose artwork isn't kept gets none.
	other := ArtworkTarget{Kind: "movie", TMDB: "999"}
	if err := s.SetTextless(ctx, other, "/nowhere1234.jpg"); err != nil {
		t.Fatal(err)
	}
	if known, kept, _ := s.TextlessKnown(ctx, other); known || kept {
		t.Errorf("a title not kept: known %v, kept %v", known, kept)
	}
}
