package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// The metadata editor: a title's texts and artwork per language offered,
// changed and given back to TMDB; only TMDB's own images of the title, or
// an upload, re-encoded; the library's list says which titles are edited.
func TestMetadata(t *testing.T) {
	ctx := context.Background()
	p, b, db, _ := signedInPanel(t)
	db.SetLanguages(ctx, []string{"fr-CA", "en-CA"}, "fr-CA")
	db.PutCatalog(ctx, []store.CatalogItem{{ID: strings.Repeat("0", 32), Kind: "movie", Name: "Heat", SortName: "a", TMDB: "949"}}, 1)
	db.RefreshTitleKeys(ctx)
	db.PutTitles(ctx, []store.Title{{Kind: "movie", TMDB: "949", Name: "Heat", Year: 1995, Overview: "TMDB's", Poster: "/heatposter1.jpg"}}, time.Now())
	p.TitleImages = func(_ context.Context, kind string, id int) (map[string][]tmdb.Image, error) {
		if kind != "movie" || id != 949 {
			return nil, tmdb.ErrNotFound
		}
		return map[string][]tmdb.Image{"poster": {{Path: "/otherposter.jpg", Language: "fr"}}, "backdrop": {}, "logo": {}}, nil
	}
	type title struct {
		Name  string
		Texts []struct {
			Language, Name, TMDBName string
			NameEdited               bool
		}
		Artwork []struct {
			Language string
			Images   map[string]struct{ URL, Edited string }
		}
	}
	read := func() title {
		t.Helper()
		rec := b.do("GET", "/admin/api/metadata/movie/949", "", nil)
		if rec.Code != 200 {
			t.Fatalf("read: %d %s", rec.Code, rec.Body)
		}
		var out title
		json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	if got := read(); got.Name != "Heat" || len(got.Texts) != 2 || len(got.Artwork) != 3 ||
		got.Artwork[1].Images["poster"].URL != "/admin/api/artwork/tmdbart_poster_heatposter1_jpg" {
		t.Fatalf("before: %+v", got)
	}

	for _, bad := range []string{
		`{"field":"name","language":"de-DE","value":"x"}`,                                    // not offered
		`{"field":"name","language":"*","value":"x"}`,                                        // texts are per language
		`{"field":"name","language":"fr-CA","value":"  "}`,                                   // empty
		`{"field":"year","language":"fr-CA","value":"1996"}`,                                 // not a field
		`{"field":"poster","language":"*","value":"/nottmdbs.jpg"}`,                          // not TMDB's for it
		`{"field":"poster","language":"*","value":"upload:` + strings.Repeat("a", 32) + `"}`, // not uploaded
	} {
		if rec := b.do("PUT", "/admin/api/metadata/movie/949", bad, nil); rec.Code != 400 {
			t.Errorf("%s: %d %s", bad, rec.Code, rec.Body)
		}
	}
	if rec := b.do("PUT", "/admin/api/metadata/episode/949", `{"field":"name","language":"fr-CA","value":"x"}`, nil); rec.Code != 404 {
		t.Errorf("not a title: %d", rec.Code)
	}

	if rec := b.do("PUT", "/admin/api/metadata/movie/949", `{"field":"name","language":"fr-CA","value":" Chaleur "}`, nil); rec.Code != 204 {
		t.Fatalf("name: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do("PUT", "/admin/api/metadata/movie/949", `{"field":"poster","language":"*","value":"/otherposter.jpg"}`, nil); rec.Code != 204 {
		t.Fatalf("poster: %d %s", rec.Code, rec.Body)
	}
	got := read()
	if got.Name != "Chaleur" || !got.Texts[0].NameEdited || got.Texts[0].TMDBName != "Heat" || got.Texts[1].Name != "Heat" {
		t.Errorf("texts: %+v", got.Texts)
	}
	if got.Artwork[0].Images["poster"].Edited != "all" || got.Artwork[2].Images["poster"].Edited != "all" ||
		got.Artwork[2].Images["poster"].URL != "/admin/api/artwork/tmdbart_poster_otherposter_jpg" {
		t.Errorf("artwork: %+v", got.Artwork)
	}
	var list struct{ Items []struct{ Edited bool } }
	json.Unmarshal(b.do("GET", "/admin/api/identification", "", nil).Body.Bytes(), &list)
	if len(list.Items) != 1 || !list.Items[0].Edited {
		t.Errorf("list: %+v", list)
	}

	// An upload, re-encoded, then chosen for English only.
	var pic bytes.Buffer
	png.Encode(&pic, image.NewRGBA(image.Rect(0, 0, 4, 2)))
	if rec := b.do("POST", metadataUploads+"logo", "not an image", map[string]string{"Content-Type": "image/png"}); rec.Code != 400 {
		t.Errorf("not an image: %d", rec.Code)
	}
	if rec := b.do("POST", metadataUploads+"logo", pic.String(), map[string]string{"Content-Type": "text/plain"}); rec.Code != 415 {
		t.Errorf("not an image type: %d", rec.Code)
	}
	if rec := b.do("POST", metadataUploads+"year", pic.String(), map[string]string{"Content-Type": "image/png"}); rec.Code != 404 {
		t.Errorf("not an image field: %d", rec.Code)
	}
	rec := b.do("POST", metadataUploads+"logo", pic.String(), map[string]string{"Content-Type": "image/png"})
	var up struct{ Value, URL string }
	if json.Unmarshal(rec.Body.Bytes(), &up); rec.Code != 201 || !strings.HasPrefix(up.Value, "upload:") {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do("PUT", "/admin/api/metadata/movie/949", `{"field":"poster","language":"en-CA","value":"`+up.Value+`"}`, nil); rec.Code != 400 {
		t.Errorf("a logo as a poster: %d", rec.Code)
	}
	if rec := b.do("PUT", "/admin/api/metadata/movie/949", `{"field":"logo","language":"en-CA","value":"`+up.Value+`"}`, nil); rec.Code != 204 {
		t.Fatalf("logo: %d %s", rec.Code, rec.Body)
	}
	got = read()
	if en := got.Artwork[2].Images["logo"]; en.Edited != "language" || en.URL != up.URL || got.Artwork[1].Images["logo"].URL != "" {
		t.Errorf("logo: %+v", got.Artwork)
	}
	if rec := b.do("GET", up.URL, "", nil); rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("upload served: %d %s", rec.Code, rec.Header())
	}

	if rec := b.do("DELETE", "/admin/api/metadata/movie/949/name/fr-CA", "", nil); rec.Code != 204 {
		t.Errorf("back to TMDB's: %d", rec.Code)
	}
	if rec := b.do("DELETE", "/admin/api/metadata/movie/949/name/fr-CA", "", nil); rec.Code != 404 {
		t.Errorf("again: %d", rec.Code)
	}
	if got := read(); got.Name != "Heat" || got.Texts[0].NameEdited {
		t.Errorf("after: %+v", got.Texts)
	}
}
