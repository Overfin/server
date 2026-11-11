package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestNoListing(t *testing.T) {
	files := fstest.MapFS{
		"index.html":       {Data: []byte("<p>page</p>")},
		"assets/panel.js":  {Data: []byte("1")},
		"assets/panel.css": {Data: []byte("2")},
	}
	h := http.FileServerFS(noListing{files})
	for path, want := range map[string]int{"/": 200, "/assets/panel.js": 200, "/assets/": 404, "/assets": 404} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != want {
			t.Errorf("%s: %d, want %d", path, rec.Code, want)
		}
	}
}
