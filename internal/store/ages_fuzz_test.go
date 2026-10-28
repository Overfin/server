package store

import (
	"strings"
	"testing"
)

// Any ratings TMDB sends give an age from 0 to 21, or none, and the
// ratings country's own rating wins when it has an age.
func FuzzTitleAge(f *testing.F) {
	f.Add(true, "US:PG-13|FR:12|DE:16", "FR")
	f.Add(false, "US:TV-MA|GB:15|GB:18", "US")
	f.Add(true, "XX:R|US:|:PG|US:NR", "US")
	f.Add(false, "SE:Från 7 år|KR:19", "KR")
	f.Fuzz(func(t *testing.T, film bool, list, country string) {
		kind := "series"
		if film {
			kind = "movie"
		}
		ratings := map[string][]string{}
		for _, part := range strings.Split(list, "|") {
			c, cert, _ := strings.Cut(part, ":")
			ratings[c] = append(ratings[c], cert)
		}
		age, ok := TitleAge(kind, ratings, country)
		if !ok {
			if age != 0 {
				t.Fatalf("no age, but %d", age)
			}
			for c, certs := range ratings {
				for _, cert := range certs {
					if _, has := RatingAge(kind, c, cert); has {
						t.Fatalf("%s %s has an age, none given", c, cert)
					}
				}
			}
			return
		}
		if age < 0 || age > 21 {
			t.Fatalf("age %d", age)
		}
		own, has := -1, false
		for _, cert := range ratings[country] {
			if a, ok := RatingAge(kind, country, cert); ok && a > own {
				own, has = a, true
			}
		}
		if has && age != own {
			t.Fatalf("age %d, the ratings country says %d", age, own)
		}
	})
}
