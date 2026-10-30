package tmdb

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// A title TMDB has no name for in the language comes with its original
// one. In a script other than Latin, its English name is taken instead;
// in Latin letters it stays (titles often keep theirs). A server in
// English asks nothing more.
func TestUnreadableNamesInEnglish(t *testing.T) {
	var english atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		en := r.URL.Query().Get("language") == "en-US"
		if en {
			english.Add(1)
		}
		answer := func(fr, enAnswer string) {
			if en {
				io.WriteString(w, enAnswer)
			} else {
				io.WriteString(w, fr)
			}
		}
		switch r.URL.Path {
		case "/3/trending/tv/week", "/3/search/multi":
			answer(`{"page":1,"total_pages":1,"results":[
				{"id":1,"media_type":"tv","name":"尚公主","original_name":"尚公主","original_language":"zh","overview":"Une princesse."},
				{"id":2,"media_type":"tv","name":"La casa de papel","original_name":"La casa de papel","original_language":"es","overview":"Un braquage."},
				{"id":3,"media_type":"tv","name":"小城良方","original_name":"小城良方","original_language":"zh","overview":""}]}`,
				`{"page":1,"total_pages":1,"results":[
				{"id":1,"media_type":"tv","name":"The Princess Royal","original_name":"尚公主","original_language":"zh"},
				{"id":2,"media_type":"tv","name":"Money Heist","original_name":"La casa de papel","original_language":"es"},
				{"id":3,"media_type":"tv","name":"小城良方","original_name":"小城良方","original_language":"zh"}]}`)
		case "/3/tv/1":
			io.WriteString(w, `{"id":1,"name":"尚公主","original_name":"尚公主","original_language":"zh","first_air_date":"2026-01-01",
				"translations":{"translations":[{"iso_639_1":"zh","data":{"name":"尚公主"}},{"iso_639_1":"en","data":{"name":"The Princess Royal"}}]}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+"/3", testToken)
	if err != nil {
		t.Fatal(err)
	}
	c.Language = func() string { return "fr" }
	ctx := context.Background()
	names := func(p *Page) []string {
		var out []string
		for _, t := range p.Titles {
			out = append(out, t.Name)
		}
		return out
	}

	page, err := c.Discover(ctx, "trending", "series", 1)
	if err != nil {
		t.Fatal(err)
	}
	// The third has no English name either: its own stays.
	if got := names(page); len(got) != 3 || got[0] != "The Princess Royal" || got[1] != "La casa de papel" || got[2] != "小城良方" {
		t.Errorf("trending: %v", got)
	}
	found, _, err := c.Search(ctx, "princesse")
	if err != nil || len(found) != 3 || found[0].Name != "The Princess Royal" {
		t.Errorf("search: %+v, %v", found, err)
	}
	full, err := c.Title(ctx, "series", 1)
	if err != nil || full.Name != "The Princess Royal" {
		t.Errorf("title: %+v, %v", full, err)
	}

	english.Store(0)
	c.Language = func() string { return "en-US" }
	if _, err := c.Discover(ctx, "trending", "series", 1); err != nil || english.Load() != 1 {
		t.Errorf("in English: %d requests, %v", english.Load(), err)
	}
}
