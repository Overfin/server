package store

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"
)

// Related titles: those with the most in common, what's rare counting
// most; the same kind only; never one with only a common genre in common;
// the same list each time.
func TestRelatedTitles(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	person := func(n int) string { return fmt.Sprintf("%032x", 0xa000+n) }
	films := []struct {
		tmdb, name string
		year       int
		genres     []int
		tags       []string
		people     []string
	}{
		{"1", "Heat", 1995, []int{80, 18}, []string{"heist", "los angeles"}, []string{person(1), person(2)}},
		{"2", "Collateral", 2004, []int{80, 18}, []string{"los angeles", "taxi"}, []string{person(1), person(3)}}, // same director, place
		{"3", "Inside Man", 2006, []int{80, 18}, []string{"heist"}, []string{person(4)}},                          // a heist
		{"4", "Some drama", 1995, []int{18}, nil, []string{person(5)}},                                            // drama alone
		{"5", "Another drama", 2001, []int{18}, nil, []string{person(6)}},
		{"6", "A comedy", 1999, []int{35}, nil, []string{person(7)}},
		{"7", "Another comedy", 2000, []int{35}, nil, []string{person(8)}},
		{"8", "Romance", 2002, []int{10749}, nil, []string{person(9)}},
	}
	var items []CatalogItem
	for i, f := range films {
		items = append(items, CatalogItem{ID: fmt.Sprintf("%032x", i+1), Kind: "movie", Name: f.name, SortName: f.name,
			TMDB: f.tmdb, Tags: f.tags, People: f.people})
	}
	// A series sharing everything with Heat: not a film, not related.
	items = append(items, CatalogItem{ID: fmt.Sprintf("%032x", 99), Kind: "series", Name: "Heat, the series", SortName: "heat",
		TMDB: "1", Tags: []string{"heist", "los angeles"}, People: []string{person(1), person(2)}})
	if err := s.PutCatalog(ctx, items, 1); err != nil {
		t.Fatal(err)
	}
	s.RefreshTitleKeys(ctx)
	for _, f := range films {
		var genres []TitleGenre
		for _, g := range f.genres {
			genres = append(genres, TitleGenre{ID: g, Name: strconv.Itoa(g)})
		}
		s.SetTitleDetails(ctx, Title{Kind: "movie", TMDB: f.tmdb, Name: f.name, Year: f.year, Genres: genres}, []byte(`{"language":"en"}`), now)
	}
	s.SetTitleDetails(ctx, Title{Kind: "series", TMDB: "1", Name: "Heat, the series", Genres: []TitleGenre{{ID: 80, Name: "80"}}}, []byte(`{}`), now)

	got, err := s.RelatedTitles(ctx, "movie/tmdb/1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"movie/tmdb/2", "movie/tmdb/3"}) {
		t.Errorf("related to Heat: %v", got)
	}
	again, _ := s.RelatedTitles(ctx, "movie/tmdb/1", 10)
	if !slices.Equal(again, got) {
		t.Errorf("not the same list: %v, then %v", got, again)
	}
	if got, _ := s.RelatedTitles(ctx, "movie/tmdb/1", 1); !slices.Equal(got, []string{"movie/tmdb/2"}) {
		t.Errorf("one: %v", got)
	}
	// A drama with nothing else in common: none.
	if got, _ := s.RelatedTitles(ctx, "movie/tmdb/4", 10); len(got) != 0 {
		t.Errorf("related to a plain drama: %v", got)
	}
	// Two comedies (rare here): related.
	if got, _ := s.RelatedTitles(ctx, "movie/tmdb/6", 10); !slices.Equal(got, []string{"movie/tmdb/7"}) {
		t.Errorf("related to a comedy: %v", got)
	}
	if got, _ := s.RelatedTitles(ctx, "movie/tmdb/404", 10); len(got) != 0 {
		t.Errorf("not in the library: %v", got)
	}
}

// Old facts answer while they're read again in the background: no one
// waits for them; the new ones show once read.
func TestRelatedRefreshedInTheBackground(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	clock := now
	s.now = func() time.Time { return clock }
	add := func(id string, people ...string) {
		t.Helper()
		item := CatalogItem{ID: fmt.Sprintf("%032s", id), Kind: "movie", Name: "F" + id, SortName: "f", TMDB: id, People: people}
		if err := s.PutCatalog(ctx, []CatalogItem{item}, 1); err != nil {
			t.Fatal(err)
		}
		s.RefreshTitleKeys(ctx)
	}
	director := fmt.Sprintf("%032x", 0xd1)
	add("1", director)
	add("2", director)
	for i := 3; i <= 12; i++ {
		add(fmt.Sprint(i), fmt.Sprintf("%032x", 0xe00+i))
	}
	if got, _ := s.RelatedTitles(ctx, "movie/tmdb/1", 10); !slices.Equal(got, []string{"movie/tmdb/2"}) {
		t.Fatalf("first: %v", got)
	}
	add("13", director)
	// Fresh: as they were.
	if got, _ := s.RelatedTitles(ctx, "movie/tmdb/1", 10); len(got) != 1 {
		t.Errorf("while fresh: %v", got)
	}
	// Old: the old ones at once, the new ones soon.
	clock = now.Add(relatedFor + time.Second)
	if got, _ := s.RelatedTitles(ctx, "movie/tmdb/1", 10); len(got) != 1 {
		t.Errorf("old, at once: %v", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _ := s.RelatedTitles(ctx, "movie/tmdb/1", 10)
		if len(got) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never read again: %v", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
