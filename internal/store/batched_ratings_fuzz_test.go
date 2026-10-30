package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// FuzzBatchedRatings: for any key, TitlesAllowed and TitleReleases answer
// as TitleAllowed and TitleRelease do for it alone.
func FuzzBatchedRatings(f *testing.F) {
	s := openTest(f)
	ctx := context.Background()
	now := time.Now()
	s.SetTitleRatings(ctx, RatedTitle{"movie", "1"}, map[string][]string{"US": {"R"}}, "1995-12-15", "1996-06-01", now)
	s.SetTitleRatings(ctx, RatedTitle{"series", "1"}, map[string][]string{"US": {"TV-Y"}}, "", "", now)
	for _, k := range []string{"movie/1", "series/1", "movie/", "/1", "movie", "", "movie/1/2", "' OR 1=1 --", "movie/1\x00"} {
		f.Add(k, 13)
	}
	f.Fuzz(func(t *testing.T, key string, age int) {
		c := Ceiling{MaxAge: age}
		allowed, err := s.TitlesAllowed(ctx, c, []string{key})
		if err != nil {
			t.Fatal(err)
		}
		releases, err := s.TitleReleases(ctx, []string{key})
		if err != nil {
			t.Fatal(err)
		}
		kind, tmdb, _ := strings.Cut(key, "/")
		one, err := s.TitleAllowed(ctx, c, kind, tmdb)
		if err != nil {
			t.Fatal(err)
		}
		released, atHome, ok, err := s.TitleRelease(ctx, kind, tmdb)
		if err != nil {
			t.Fatal(err)
		}
		// A key that isn't valid UTF-8 reaches SQLite's JSON as U+FFFD and
		// matches nothing; alone, its bytes match nothing either.
		if allowed[key] != one {
			t.Errorf("%q, age %d: %v at once, %v alone", key, age, allowed[key], one)
		}
		r, got := releases[key]
		if got != ok || r.Released != released || r.AtHome != atHome {
			t.Errorf("%q: %+v %v at once, %q %q %v alone", key, r, got, released, atHome, ok)
		}
	})
}
