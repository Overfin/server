package tmdb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A collection: its films checked, each once, in release order (those
// without a date last); in a regional language, what its translation
// lacks comes from the language alone, in one more request. A film's
// collection comes from its page alone.
func TestCollection(t *testing.T) {
	asked := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path + "?" + r.URL.Query().Get("language")
		asked[key]++
		switch key {
		case "/collection/10?fr-CA":
			io.WriteString(w, `{"id":10,"name":"Star Wars - La collection","overview":"","poster_path":"/swposter123.jpg",
				"backdrop_path":"../escape.jpg","parts":[
				{"id":3,"title":"Le Retour du Jedi","original_title":"Return of the Jedi","original_language":"en","release_date":"1983-05-25","overview":"x"},
				{"id":1,"title":"Star Wars","original_title":"Star Wars","original_language":"en","release_date":"1977-05-25","overview":"y"},
				{"id":9,"title":"Sans date","original_title":"Sans date","original_language":"fr","release_date":"","overview":"z"},
				{"id":2,"title":"L'Empire contre-attaque","original_title":"The Empire Strikes Back","original_language":"en","release_date":"1980-05-17","overview":"w"},
				{"id":3,"title":"Le Retour du Jedi","release_date":"1983-05-25"},
				{"id":0,"title":"Not a film"}]}`)
		case "/collection/10?fr":
			io.WriteString(w, `{"id":10,"name":"Star Wars","overview":"La saga.","parts":[
				{"id":1,"title":"La Guerre des étoiles","original_title":"Star Wars","overview":"y"}]}`)
		case "/movie/2?":
			io.WriteString(w, `{"id":2,"belongs_to_collection":{"id":10,"name":"Star Wars"}}`)
		case "/movie/5?":
			io.WriteString(w, `{"id":5,"belongs_to_collection":null}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, err := New(srv.URL, testToken)
	if err != nil {
		t.Fatal(err)
	}
	c.Language = func() string { return "fr-CA" }
	ctx := context.Background()
	got, err := c.Collection(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Star Wars - La collection" || got.Overview != "La saga." || got.Poster != "/swposter123.jpg" || got.Backdrop != "" {
		t.Errorf("collection: %+v", got)
	}
	var names []string
	for _, p := range got.Parts {
		names = append(names, p.Name)
	}
	want := []string{"La Guerre des étoiles", "L'Empire contre-attaque", "Le Retour du Jedi", "Sans date"}
	if len(names) != len(want) {
		t.Fatalf("films: %v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("films: %v, want %v", names, want)
			break
		}
	}
	if got.Released[2] != "1980-05-17" || got.Released[9] != "" {
		t.Errorf("released: %v", got.Released)
	}
	if asked["/collection/10?fr"] != 1 {
		t.Errorf("the language alone asked %d times", asked["/collection/10?fr"])
	}
	if _, err := c.Collection(ctx, 11); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown collection: %v", err)
	}
	if _, err := c.Collection(ctx, -1); !errors.Is(err, ErrNotFound) {
		t.Errorf("invalid ID: %v", err)
	}

	c.Language = nil
	if id, err := c.FilmCollection(ctx, 2); id != 10 || err != nil {
		t.Errorf("film in a collection: %d %v", id, err)
	}
	if id, err := c.FilmCollection(ctx, 5); id != 0 || err != nil {
		t.Errorf("film in none: %d %v", id, err)
	}
	if _, err := c.FilmCollection(ctx, 6); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown film: %v", err)
	}
}

// A text-free poster: the first poster without a language.
func TestTextlessPoster(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/tv/1399/images" && r.URL.Query().Get("include_image_language") == "null":
			io.WriteString(w, `{"id":1399,"posters":[{"file_path":"../bad.jpg","iso_639_1":null},{"file_path":"/textless123.jpg","iso_639_1":null}]}`)
		case r.URL.Path == "/movie/5/images":
			io.WriteString(w, `{"id":5,"posters":[{"file_path":"/frposter12.jpg","iso_639_1":"fr"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, _ := New(srv.URL, testToken)
	ctx := context.Background()
	if got, err := c.TextlessPoster(ctx, "series", 1399); got != "/textless123.jpg" || err != nil {
		t.Errorf("series: %q %v", got, err)
	}
	if got, err := c.TextlessPoster(ctx, "movie", 5); got != "" || err != nil {
		t.Errorf("only posters with text: %q %v", got, err)
	}
	if _, err := c.TextlessPoster(ctx, "movie", 404); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
}
