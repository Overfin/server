package tmdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Whatever TMDB answers for a collection, what Overfin keeps of it is
// checked: its ID the one asked, its name plain and capped, image paths of
// the one shape, its films each once, valid, and capped in number.
func FuzzCollection(f *testing.F) {
	f.Add([]byte(`{"id":10,"name":"Saga","overview":"x","poster_path":"/swposter123.jpg","parts":[{"id":1,"title":"A","release_date":"1977-05-25"}]}`))
	f.Add([]byte(`{"id":10,"name":"<b>x</b>\u202e","backdrop_path":"../escape.jpg","parts":[{"id":-1,"title":"A"},{"id":1,"title":""},{"id":1,"title":"B"},{"id":1,"title":"B"}]}`))
	f.Add([]byte(`{"id":11,"name":"Other"}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write(body)
		}))
		defer srv.Close()
		c, err := New(srv.URL, testToken)
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.Collection(context.Background(), 10)
		if err != nil {
			return
		}
		if got.ID != 10 || got.Name == "" || len(got.Name) > 300 || len(got.Overview) > 2000 ||
			(got.Poster != "" && !imagePath.MatchString(got.Poster)) || (got.Backdrop != "" && !imagePath.MatchString(got.Backdrop)) ||
			len(got.Parts) > maxParts {
			t.Fatalf("collection %+v", got)
		}
		seen := map[int]bool{}
		for _, p := range got.Parts {
			if !ValidID(p.TMDBID) || p.Kind != "movie" || p.Name == "" || seen[p.TMDBID] ||
				(p.Poster != "" && !imagePath.MatchString(p.Poster)) || (p.Backdrop != "" && !imagePath.MatchString(p.Backdrop)) {
				t.Fatalf("film %+v", p)
			}
			seen[p.TMDBID] = true
		}
	})
}
