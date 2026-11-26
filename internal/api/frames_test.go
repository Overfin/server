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

// An episode's frame (catalog.EpisodeFrames) is served by its ID to a
// profile that sees the episode, at a still's widths, cached as never
// changing; to a profile limited to another library, and for an ID that
// isn't one, "not found".
func TestEpisodeFrameServed(t *testing.T) {
	var st *store.Store
	srv, fake := newServerWith(t, options{store: &st})
	fake.partnerSeesAll = true
	ctx := context.Background()
	id := func(n int) string { return fmt.Sprintf("%032x", n) }
	n := func(i int) *int { return &i }
	films, music := id(0xf1), id(0xf2)
	st.PutCatalog(ctx, []store.CatalogItem{
		{ID: id(1), Kind: "series", Name: "GoT", SortName: "got", TMDB: "1399", LibraryID: films},
		{ID: id(3), Kind: "episode", Name: "E1", SortName: "e", SeriesID: id(1), SeasonNumber: n(1), EpisodeNumber: n(1), LibraryID: films},
	}, 1)
	st.RefreshTitleKeys(ctx)
	st.SetCatalogState(ctx, store.CatalogState{Pass: 1})
	now := time.Now()
	det, _ := json.Marshal(store.TitleDetails{Seasons: []store.SeasonInfo{{Number: 1, EpisodeCount: 1}}})
	st.SetTitleDetails(ctx, store.Title{Kind: "series", TMDB: "1399", Name: "Le Trône de fer"}, det, now)
	st.SetSeason(ctx, "1399", store.Season{Number: 1, Name: "Saison 1", Episodes: []store.Episode{{Number: 1, Name: "L'hiver vient", AirDate: "2011-04-17"}}}, now)
	candidates, err := st.FrameCandidates(ctx, now, 10)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates: %+v %v", candidates, err)
	}
	var b bytes.Buffer
	jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 640, 360)), nil)
	frameID := strings.Repeat("ab", 16)
	if err := st.PutFrame(ctx, candidates[0].TitleKey, candidates[0].Item, frameID, b.Bytes(), now); err != nil {
		t.Fatal(err)
	}
	url := "/api/v1/images/epframe_" + frameID

	req, _ := http.NewRequest("GET", srv.URL+url, nil)
	req.Header.Set("Remote-User", subject)
	res, err := client(srv, subject).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/jpeg" || !strings.Contains(res.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("the frame: %d %v", res.StatusCode, res.Header)
	}
	// Any positive width rounds up to one a still has; none other.
	if code, _ := getAs(t, srv, subject, url+"?width=13"); code != 200 {
		t.Errorf("width 13: %d", code)
	}
	for _, q := range []string{"?width=0", "?width=-5", "?width=x", "?size=huge", "?size=small&width=300", "?other=1"} {
		if code, _ := getAs(t, srv, subject, url+q); code != http.StatusNotFound && code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, code)
		}
	}
	for _, other := range []string{"epframe_" + strings.Repeat("cd", 16), "epframe_abab", "epframe_"} {
		if code, _ := getAs(t, srv, subject, "/api/v1/images/"+other); code != http.StatusNotFound {
			t.Errorf("%s: %d", other, code)
		}
	}
	// A profile limited to the music library doesn't see the series.
	limitedProfile(t, srv, music)
	if code, _ := getAs(t, srv, subject, url); code != http.StatusNotFound {
		t.Errorf("a profile that doesn't see the episode: %d", code)
	}
}
