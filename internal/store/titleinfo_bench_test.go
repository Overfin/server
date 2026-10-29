package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"
)

// benchTitles makes a library of n films with pages as TMDB's are (a cast
// of 60, genres, names), ratings and artwork, and returns their keys.
func benchTitles(b *testing.B, n int) (*Store, []string) {
	b.Helper()
	s := openTest(&testing.T{})
	ctx := context.Background()
	now := time.Now()
	var items []CatalogItem
	var keys []string
	cast := make([]CastMember, 60)
	for i := range cast {
		cast[i] = CastMember{Name: "Someone " + strconv.Itoa(i), Character: "A part", Profile: "/profile1234567.jpg", TMDB: strconv.Itoa(1000 + i)}
	}
	details, _ := json.Marshal(TitleDetails{Genres: []string{"Drame", "Crime"}, Runtime: 120, Tagline: "A tagline.", Cast: cast,
		Names: []string{"Another name", "Yet another"}, Language: "en"})
	for i := 1; i <= n; i++ {
		id := strconv.Itoa(i)
		items = append(items, CatalogItem{ID: fmt.Sprintf("%032x", i), Kind: "movie", Name: "F" + id, SortName: "f", TMDB: id, LibraryID: fmt.Sprintf("%032x", 0xf1)})
		keys = append(keys, "movie/tmdb/"+id)
	}
	s.PutCatalog(ctx, items, 1)
	s.RefreshTitleKeys(ctx)
	for i := 1; i <= n; i++ {
		id := strconv.Itoa(i)
		s.SetTitleDetails(ctx, Title{Kind: "movie", TMDB: id, Name: "Film " + id, Year: 2000, Overview: "Something happens.",
			Genres: []TitleGenre{{ID: 18, Name: "Drame"}}}, details, now)
		s.SetTitleRatings(ctx, RatedTitle{Kind: "movie", TMDB: id}, map[string][]string{"US": {"R"}, "FR": {"12"}}, "2000-01-01", "2000-01-01", now)
		target := ArtworkTarget{Kind: "movie", TMDB: id}
		s.SetArtwork(ctx, target, map[string]map[string]string{target.Key(): {"poster": "/poster1234567.jpg", "backdrop": "/backdrop12345.jpg", "logo": "/logo123456.png", "textless": ""}}, now)
	}
	return s, keys
}

func BenchmarkTitleInfos(b *testing.B) {
	s, keys := benchTitles(b, 2000)
	ctx := context.Background()
	for _, n := range []int{20, 60} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if _, err := s.TitleInfos(ctx, keys[i%1900:i%1900+n], "2026-10-29", ""); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
