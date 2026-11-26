package api_test

import (
	"net/http"
	"testing"
)

// Every language Overfin names, each in itself, for a profile choosing
// its audio or subtitles: kept by the browser an hour, its own only;
// nobody signed in, nothing.
func TestLanguagesList(t *testing.T) {
	srv, _ := newServerWith(t, options{})
	var out struct {
		Languages []struct{ Tag, Name string }
	}
	if code := get(t, srv, "/api/v1/languages", &out); code != 200 || len(out.Languages) < 50 {
		t.Fatalf("languages: %d %d", code, len(out.Languages))
	}
	seen := map[string]bool{}
	for _, l := range out.Languages {
		if l.Tag == "" || l.Name == "" || seen[l.Tag] {
			t.Errorf("a language: %+v", l)
		}
		seen[l.Tag] = true
	}
	if !seen["fr"] || !seen["en"] {
		t.Error("French or English missing")
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/languages", nil)
	req.Header.Set("Remote-User", subject)
	res, err := client(srv, subject).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if cc := res.Header.Get("Cache-Control"); res.StatusCode != 200 || cc != "private, max-age=3600" {
		t.Errorf("cache: %d %q", res.StatusCode, cc)
	}
	res, err = http.Get(srv.URL + "/api/v1/languages")
	if err == nil {
		res.Body.Close()
		if res.StatusCode == 200 {
			t.Error("listed to nobody signed in")
		}
	}
}
