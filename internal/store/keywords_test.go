package store

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// A title's keywords come with its page, are replaced when it's fetched
// again, are listed once whatever their case for choosing which to hide,
// and go with the title.
func TestTitleKeywords(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "movie", Name: "Akira", SortName: "a", TMDB: "149"},
		{ID: id(1), Kind: "series", Name: "Frieren", SortName: "f", TMDB: "209867"},
	}, 1)
	now := time.Now()
	s.SetTitleDetails(ctx, Title{Kind: "movie", TMDB: "149", Name: "Akira", Keywords: []string{"Anime", "cyberpunk"}}, []byte(`{}`), now)
	s.SetTitleDetails(ctx, Title{Kind: "series", TMDB: "209867", Name: "Frieren", Keywords: []string{"anime", "elf"}}, []byte(`{}`), now)
	// A title no library has: its keywords aren't the library's.
	s.SetTitleDetails(ctx, Title{Kind: "movie", TMDB: "1", Name: "Elsewhere", Keywords: []string{"zombie"}}, []byte(`{}`), now)
	if tags, err := s.CatalogTags(ctx); err != nil || !slices.Equal(tags, []string{"Anime", "cyberpunk", "elf"}) {
		t.Errorf("tags: %v %v", tags, err)
	}
	s.SetTitleDetails(ctx, Title{Kind: "movie", TMDB: "149", Name: "Akira", Keywords: []string{"cyberpunk"}}, []byte(`{}`), now)
	if tags, _ := s.CatalogTags(ctx); !slices.Equal(tags, []string{"anime", "cyberpunk", "elf"}) {
		t.Errorf("fetched again: %v", tags)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM catalog_titles WHERE kind = 'series'`); err != nil {
		t.Fatal(err)
	}
	if tags, _ := s.CatalogTags(ctx); !slices.Equal(tags, []string{"cyberpunk"}) {
		t.Errorf("after the title went: %v", tags)
	}
}

// Titles no library has, for a profile with tags hidden: shown only when
// Overfin already has their keywords and none is hidden; a title only
// listed (no details fetched) or unknown stays hidden.
func TestTitlesTagFree(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	s.SetTitleDetails(ctx, Title{Kind: "movie", TMDB: "1", Name: "Clean", Keywords: []string{"friendship"}}, []byte(`{}`), now)
	s.SetTitleDetails(ctx, Title{Kind: "movie", TMDB: "2", Name: "Gory", Keywords: []string{"Zombie", "friendship"}}, []byte(`{}`), now)
	s.SetTitleDetails(ctx, Title{Kind: "series", TMDB: "3", Name: "No keywords"}, []byte(`{}`), now)
	if err := s.PutTitles(ctx, []Title{{Kind: "movie", TMDB: "4", Name: "Only listed"}}, now); err != nil {
		t.Fatal(err)
	}
	got, err := s.TitlesTagFree(ctx, []string{"zombie"}, []string{"movie/1", "movie/2", "series/3", "movie/4", "movie/5", "series/1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got["movie/1"] || !got["series/3"] {
		t.Errorf("tag free: %v", got)
	}
}
