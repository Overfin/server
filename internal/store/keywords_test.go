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
