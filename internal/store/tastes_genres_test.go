package store

import (
	"context"
	"slices"
	"testing"
	"time"
)

// The genres people choose: a joint series genre gives way to its film
// genres when they're there and stays when they aren't; series genres
// TMDB leaves in English get a French name.
func TestTasteChoices(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	title := func(kind, tmdb string, genres ...TitleGenre) {
		t.Helper()
		if err := s.SetTitleDetails(ctx, Title{Kind: kind, TMDB: tmdb, Name: "T" + tmdb, Genres: genres}, []byte(`{}`), now); err != nil {
			t.Fatal(err)
		}
	}
	title("movie", "1", TitleGenre{28, "Action"})
	title("series", "2", TitleGenre{10759, "Action & Adventure"}, TitleGenre{10765, "Sci-Fi & Fantasy"})
	title("series", "3", TitleGenre{10762, "Kids"}, TitleGenre{10764, "Reality"})
	names := func(lang string) []string {
		t.Helper()
		got, err := s.TasteChoices(ctx, lang)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, g := range got {
			out = append(out, g.Name)
		}
		return out
	}
	// Action is there: its joint series genre goes. Nothing of science
	// fiction or fantasy for films: the joint one stays.
	if got := names("fr"); !slices.Equal(got, []string{"Action", "Enfants", "Sci-Fi & Fantasy", "Téléréalité"}) {
		t.Errorf("fr: %v", got)
	}
	if got := names("en-CA"); !slices.Equal(got, []string{"Action", "Kids", "Reality", "Sci-Fi & Fantasy"}) {
		t.Errorf("en: %v", got)
	}
}

// What a choice counts for: a film genre also as its joint series genre;
// a joint one chosen before as its film genres, and shown as them.
func TestLikedGenres(t *testing.T) {
	sorted := func(l []int) []int { slices.Sort(l); return l }
	if got := sorted(Tastes{Genres: []int{28, 18}}.LikedGenres()); !slices.Equal(got, []int{18, 28, 10759}) {
		t.Errorf("action and drama: %v", got)
	}
	if got := sorted(Tastes{Genres: []int{10765}}.LikedGenres()); !slices.Equal(got, []int{14, 878, 10765}) {
		t.Errorf("sci-fi & fantasy: %v", got)
	}
	if got := (Tastes{Genres: []int{10765, 878, 35}}).ChosenGenres(); !slices.Equal(got, []int{878, 14, 35}) {
		t.Errorf("chosen: %v", got)
	}
}
