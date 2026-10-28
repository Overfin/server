package tmdb

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRegionalFallsBackToLanguage: asked in fr-CA, TMDB answers with the
// Canadian translation only, so a title without one comes with its
// original name and no overview. The client fills those from French.
func TestRegionalFallsBackToLanguage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fr := r.URL.Query().Get("language") == "fr"
		answer := func(ca, frAnswer string) {
			if fr {
				io.WriteString(w, frAnswer)
			} else {
				io.WriteString(w, ca)
			}
		}
		switch r.URL.Path {
		case "/3/trending/tv/week":
			answer(`{"page":1,"total_pages":1,"results":[
				{"id":220542,"media_type":"tv","name":"薬屋のひとりごと","original_name":"薬屋のひとりごと","original_language":"ja","overview":""},
				{"id":120911,"media_type":"tv","name":"La liste terminale","original_name":"The Terminal List","original_language":"en","overview":"Québec."}]}`,
				`{"page":1,"total_pages":1,"results":[
				{"id":220542,"media_type":"tv","name":"Les Carnets de l'apothicaire","original_name":"薬屋のひとりごと","original_language":"ja","overview":"Mao Mao."},
				{"id":120911,"media_type":"tv","name":"The Terminal List","original_name":"The Terminal List","original_language":"en","overview":"France."}]}`)
		case "/3/movie/1283515":
			answer(`{"id":1283515,"title":"Verity","original_title":"Verity","original_language":"en","release_date":"2026-01-01","overview":"","tagline":""}`,
				`{"id":1283515,"title":"Verity","original_title":"Verity","original_language":"en","release_date":"2026-01-01","overview":"Une autrice.","tagline":"Lisez."}`)
		case "/3/search/multi":
			answer(`{"results":[{"id":220542,"media_type":"tv","name":"薬屋のひとりごと","original_name":"薬屋のひとりごと","original_language":"ja"}]}`,
				`{"results":[{"id":220542,"media_type":"tv","name":"Les Carnets de l'apothicaire","original_name":"薬屋のひとりごと","original_language":"ja"}]}`)
		case "/3/tv/220542/season/1":
			answer(`{"season_number":1,"name":"Saison 1","overview":"","episodes":[{"episode_number":1,"name":"Épisode 1","overview":""},{"episode_number":2,"name":"Le poison","overview":"Au Québec."}]}`,
				`{"season_number":1,"name":"Saison 1","overview":"La cour.","episodes":[{"episode_number":1,"name":"Mao Mao","overview":"Une apothicaire."},{"episode_number":2,"name":"Le venin","overview":"En France."}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+"/3", testToken)
	if err != nil {
		t.Fatal(err)
	}
	c.Language = func() string { return "fr-CA" }
	ctx := context.Background()

	page, err := c.Discover(ctx, "trending", "series", 1)
	if err != nil || len(page.Titles) != 2 {
		t.Fatalf("trending: %+v, %v", page, err)
	}
	if got := page.Titles[0]; got.Name != "Les Carnets de l'apothicaire" || got.Overview != "Mao Mao." {
		t.Errorf("without a Canadian translation: %q, %q", got.Name, got.Overview)
	}
	if got := page.Titles[1]; got.Name != "La liste terminale" || got.Overview != "Québec." {
		t.Errorf("the Canadian translation is kept: %q, %q", got.Name, got.Overview)
	}

	d, err := c.Details(ctx, "movie", 1283515)
	if err != nil || d.Overview != "Une autrice." {
		t.Errorf("details: %+v, %v", d, err)
	}
	full, err := c.Title(ctx, "movie", 1283515)
	if err != nil || full.Overview != "Une autrice." || full.Tagline != "Lisez." {
		t.Errorf("title: %+v, %v", full, err)
	}

	titles, _, err := c.Search(ctx, "apothicaire")
	if err != nil || len(titles) != 1 || titles[0].Name != "Les Carnets de l'apothicaire" {
		t.Errorf("search: %+v, %v", titles, err)
	}

	se, err := c.Season(ctx, 220542, 1)
	if err != nil || se.Overview != "La cour." || len(se.Episodes) != 2 {
		t.Fatalf("season: %+v, %v", se, err)
	}
	if e := se.Episodes[0]; e.Name != "Mao Mao" || e.Overview != "Une apothicaire." {
		t.Errorf("episode without a Canadian translation: %+v", e)
	}
	if e := se.Episodes[1]; e.Name != "Le poison" || e.Overview != "Au Québec." {
		t.Errorf("the Canadian translation is kept: %+v", e)
	}
}

// TestPickImage: TMDB tags images with a language alone (and a region), so
// in fr-CA a French logo is the one, a Canadian one first.
func TestPickImage(t *testing.T) {
	logos := []rawImage{{"/enlogo1234.png", "en", "US"}, {"/frlogo1234.png", "fr", "FR"}, {"/qclogo1234.png", "fr", "CA"}, {"/nolang1234.png", "", ""}}
	for _, tc := range []struct {
		lang   string
		images []rawImage
		want   string
	}{
		{"fr-CA", logos, "/qclogo1234.png"},
		{"fr-FR", logos, "/frlogo1234.png"},
		{"fr", logos, "/frlogo1234.png"},
		{"fr-CA", logos[:2], "/frlogo1234.png"},
		{"de-DE", logos, "/nolang1234.png"},
		{"de-DE", logos[:1], "/enlogo1234.png"},
		{"", logos, "/nolang1234.png"},
	} {
		if got := pick(tc.images, tc.lang); got != tc.want {
			t.Errorf("pick in %q: %q, want %q", tc.lang, got, tc.want)
		}
	}
}

// A logo is the title written as art: in the language, else without text,
// else in the other languages given (the server's, then the original), in
// turn, else none.
func TestPickLogo(t *testing.T) {
	logos := []rawImage{{"/enlogo1234.png", "en", "US"}, {"/frlogo1234.png", "fr", "FR"}, {"/nolang1234.png", "", ""}}
	for _, tc := range []struct {
		lang   string
		images []rawImage
		want   string
	}{
		{"fr-CA", logos, "/frlogo1234.png"},
		{"de-DE", logos, "/nolang1234.png"},
		{"de-DE", logos[:2], ""},
		{"", logos, "/enlogo1234.png"},
		{"en-GB", logos[1:], "/nolang1234.png"},
		{"de-DE en", logos[:2], "/enlogo1234.png"},
		{"de-DE ja fr", logos[:2], "/frlogo1234.png"},
		{"de-DE de", logos[:2], ""},
	} {
		lang, others, _ := strings.Cut(tc.lang, " ")
		if got := pickLogo(tc.images, lang, strings.Fields(others)...); got != tc.want {
			t.Errorf("logo in %q: %q, want %q", tc.lang, got, tc.want)
		}
	}
}
