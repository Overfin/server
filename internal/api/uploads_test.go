package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

// Artwork an admin uploaded for a title (the metadata editor) shows on it,
// and is served only to profiles that may see the title.
func TestUploadedArtwork(t *testing.T) {
	var st *store.Store
	srv, fake := newServerWith(t, options{store: &st})
	fake.partnerSeesAll = true
	ctx := context.Background()
	films, music := fmt.Sprintf("%032x", 0xf1), fmt.Sprintf("%032x", 0xf2)
	st.PutCatalog(ctx, []store.CatalogItem{
		{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "Heat", SortName: "Heat", TMDB: "949", LibraryID: music},
		{ID: fmt.Sprintf("%032x", 2), Kind: "movie", Name: "Other", SortName: "Other", TMDB: "950", LibraryID: films},
	}, 1)
	st.RefreshTitleKeys(ctx)
	st.SetCatalogState(ctx, store.CatalogState{Pass: 1})
	id, unused := strings.Repeat("a", 32), strings.Repeat("b", 32)
	var poster bytes.Buffer
	jpeg.Encode(&poster, image.NewRGBA(image.Rect(0, 0, 1000, 1500)), nil)
	for _, x := range []string{id, unused} {
		if err := st.PutTitleUpload(ctx, x, store.TitleUpload{Type: "poster", ContentType: "image/jpeg", Data: poster.Bytes()}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetTitleEdit(ctx, "movie", "949", "poster", store.AllLanguages, store.UploadValue(id), time.Now()); err != nil {
		t.Fatal(err)
	}

	var it struct {
		Images struct{ Primary struct{ URL string } }
	}
	if code := get(t, srv, "/api/v1/titles/title_movie_tmdb_949", &it); code != 200 || it.Images.Primary.URL != "/api/v1/images/titleup_"+id {
		t.Fatalf("title: %d %+v", code, it)
	}
	if code := get(t, srv, it.Images.Primary.URL, nil); code != 200 {
		t.Errorf("owner: %d", code)
	}
	// At the width asked, made from the upload; the width it's kept at
	// beyond.
	for query, want := range map[string]int{"?width=300": 400, "?size=small": 200, "?width=1900": 1000, "?width=x": 0, "?size=big": 0} {
		req, _ := http.NewRequest("GET", srv.URL+it.Images.Primary.URL+query, nil)
		req.Header.Set("Remote-User", subject)
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		cfg, _, decodeErr := image.DecodeConfig(res.Body)
		res.Body.Close()
		if (want == 0 && res.StatusCode != 404) || (want > 0 && (decodeErr != nil || cfg.Width != want)) {
			t.Errorf("%s: %d, %d wide, want %d", query, res.StatusCode, cfg.Width, want)
		}
	}
	for _, path := range []string{"/api/v1/images/titleup_" + unused, "/api/v1/images/titleup_nothex", "/api/v1/images/titleup_" + id + "?size=huge"} {
		if code := get(t, srv, path, nil); code != 404 {
			t.Errorf("%s: %d", path, code)
		}
	}

	// A profile without the title's library doesn't get it.
	_, body := post(t, srv, "/api/v1/profiles", `{"name":"Partner"}`)
	var partner struct{ ID string }
	json.Unmarshal([]byte(body), &partner)
	if code, body := put(t, srv, "PATCH", "/api/v1/profiles/"+partner.ID, `{"libraries":["lib_`+films+`"]}`); code != 200 {
		t.Fatalf("limit: %d %s", code, body)
	}
	if code, body := post(t, srv, "/api/v1/profiles/"+partner.ID+"/activate", `{}`); code != 200 {
		t.Fatalf("activate: %d %s", code, body)
	}
	if code := get(t, srv, it.Images.Primary.URL, nil); code != 404 {
		t.Errorf("partner: %d", code)
	}
}
