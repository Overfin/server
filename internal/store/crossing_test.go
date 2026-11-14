package store

import (
	"context"
	"slices"
	"testing"
	"time"
)

// A title in a language the server speaks stays whatever its votes; one in
// another language needs minVotes (499 isn't enough, 500 is); one whose
// language isn't known stays. The order is kept, and what a later list
// leaves out (no language, no votes) doesn't erase what's known.
func TestCrossing(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Now()
	if err := s.PutTitles(ctx, []Title{
		{Kind: "movie", TMDB: "1", Name: "Local", ListLanguage: "fr", ListVotes: 0},
		{Kind: "movie", TMDB: "2", Name: "Regional", ListLanguage: "hi", ListVotes: 499},
		{Kind: "movie", TMDB: "3", Name: "Crossover", ListLanguage: "ko", ListVotes: 500},
		{Kind: "movie", TMDB: "4", Name: "Unknown"},
		{Kind: "movie", TMDB: "5", Name: "English", ListLanguage: "en", ListVotes: 3},
	}, now); err != nil {
		t.Fatal(err)
	}
	ids := []string{"5", "4", "3", "2", "1"}
	got, err := s.Crossing(ctx, "movie", ids, []string{"fr", "en"}, 500)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"5", "4", "3", "1"}; !slices.Equal(got, want) {
		t.Errorf("kept %v, want %v", got, want)
	}

	// Seen again in a list that doesn't say (Seerr's): still known.
	if err := s.PutTitles(ctx, []Title{{Kind: "movie", TMDB: "2", Name: "Regional"}}, now); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Crossing(ctx, "movie", []string{"2"}, []string{"fr"}, 500); len(got) != 0 {
		t.Errorf("forgot what was known: %v", got)
	}
	// More votes later: in.
	if err := s.PutTitles(ctx, []Title{{Kind: "movie", TMDB: "2", Name: "Regional", ListLanguage: "hi", ListVotes: 800}}, now); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Crossing(ctx, "movie", []string{"2"}, []string{"fr"}, 500); !slices.Equal(got, []string{"2"}) {
		t.Errorf("crossed later, still out: %v", got)
	}
	// Same IDs, other kind: none of them known, all stay.
	if got, _ := s.Crossing(ctx, "series", ids, []string{"fr"}, 500); !slices.Equal(got, ids) {
		t.Errorf("series: %v", got)
	}
}
