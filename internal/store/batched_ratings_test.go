package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TitlesAllowed and TitleReleases answer for many titles what TitleAllowed
// and TitleRelease answer for one.
func TestBatchedRatingsAgree(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	s.SetTitleRatings(ctx, RatedTitle{"movie", "1"}, map[string][]string{"US": {"R"}}, "1995-12-15", "1996-06-01", now)
	s.SetTitleRatings(ctx, RatedTitle{"movie", "2"}, map[string][]string{"US": {"G"}}, "2099-01-01", "", now)
	s.SetTitleRatings(ctx, RatedTitle{"series", "1"}, map[string][]string{"US": {"TV-Y"}}, "", "", now)
	s.SetTitleRatings(ctx, RatedTitle{"movie", "3"}, map[string][]string{"US": {NotRated}}, "2000-01-01", "", now)
	keys := []string{"movie/1", "movie/2", "series/1", "movie/3", "movie/4", "series/9", "movie/1"}
	for _, age := range []int{0, 7, 13, 18} {
		c := Ceiling{MaxAge: age}
		allowed, err := s.TitlesAllowed(ctx, c, keys)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range keys {
			kind, tmdb, _ := strings.Cut(k, "/")
			one, err := s.TitleAllowed(ctx, c, kind, tmdb)
			if err != nil {
				t.Fatal(err)
			}
			if allowed[k] != one {
				t.Errorf("age %d, %s: %v at once, %v alone", age, k, allowed[k], one)
			}
		}
	}
	releases, err := s.TitleReleases(ctx, keys)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		kind, tmdb, _ := strings.Cut(k, "/")
		released, atHome, ok, err := s.TitleRelease(ctx, kind, tmdb)
		if err != nil {
			t.Fatal(err)
		}
		r, got := releases[k]
		if got != ok || r.Released != released || r.AtHome != atHome {
			t.Errorf("%s: %+v %v at once, %q %q %v alone", k, r, got, released, atHome, ok)
		}
	}
	if len(releases) != 4 {
		t.Errorf("releases: %v", releases)
	}
}
