package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/overfin/server/internal/tmdb"
)

// The images TMDB has of a title, to pick from: each kind's, with the
// panel's address for it; no TMDB to ask, a title TMDB doesn't know, or
// one that can't be a title: each said apart; TMDB failing, a failure.
func TestMetadataImages(t *testing.T) {
	p, b, _, _ := signedInPanel(t)
	var asked []string
	var answer error
	p.TitleImages = func(_ context.Context, kind string, id int) (map[string][]tmdb.Image, error) {
		asked = append(asked, kind)
		if answer != nil {
			return nil, answer
		}
		return map[string][]tmdb.Image{"poster": {{Path: "/p1234567.jpg", Language: "fr", Width: 500, Height: 750}}, "logo": {}}, nil
	}
	rec := b.do("GET", "/admin/api/metadata/movie/603/images", "", nil)
	var out map[string][]struct {
		Value, URL, Language string
		Width, Height        int
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || len(out["poster"]) != 1 || out["poster"][0].Value != "/p1234567.jpg" || out["poster"][0].URL == "" ||
		out["poster"][0].Language != "fr" || out["logo"] == nil {
		t.Fatalf("images: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(out["poster"][0].URL, "p1234567") {
		t.Errorf("the panel's address: %s", out["poster"][0].URL)
	}
	for _, path := range []string{"/admin/api/metadata/book/603/images", "/admin/api/metadata/movie/0603/images",
		"/admin/api/metadata/movie/x/images", "/admin/api/metadata/movie/-1/images"} {
		if rec := b.do("GET", path, "", nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
	if len(asked) != 1 {
		t.Errorf("TMDB asked for bad titles: %v", asked)
	}
	answer = tmdb.ErrNotFound
	if rec := b.do("GET", "/admin/api/metadata/series/1399/images", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("a title TMDB doesn't know: %d", rec.Code)
	}
	answer = errors.New("tmdb: 503")
	if rec := b.do("GET", "/admin/api/metadata/series/1399/images", "", nil); rec.Code < 500 {
		t.Errorf("TMDB failing: %d", rec.Code)
	}
	p.TitleImages = nil
	if rec := b.do("GET", "/admin/api/metadata/movie/603/images", "", nil); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no-tmdb") {
		t.Errorf("no TMDB: %d %s", rec.Code, rec.Body)
	}
}
