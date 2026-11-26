package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

// A profile limited to some libraries, with a tag hidden from it, sees
// only that, from the index and from Jellyfin alike.
func TestLibrariesAndHiddenTags(t *testing.T) {
	var st *store.Store
	srv, fake := newServerWith(t, options{store: &st})
	fake.partnerSeesAll = true // the second profile below
	ctx := context.Background()
	films, music := fmt.Sprintf("%032x", 0xf1), fmt.Sprintf("%032x", 0xf2)
	// Films 1 to 5 in the films library (5 tagged Horror on TMDB), 6 in
	// music; 7 isn't in the index at all (Jellyfin puts it in music).
	var items []store.CatalogItem
	for i := 1; i <= 6; i++ {
		it := store.CatalogItem{ID: fmt.Sprintf("%032x", i), Kind: "movie", Name: fmt.Sprint(i), SortName: fmt.Sprint(i), LibraryID: films}
		if i == 5 {
			it.TMDB = "5"
		}
		if i == 6 {
			it.LibraryID = music
		}
		items = append(items, it)
	}
	if err := st.PutCatalog(ctx, items, 1); err != nil {
		t.Fatal(err)
	}
	st.RefreshTitleKeys(ctx)
	st.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "5", Name: "5", Keywords: []string{"Horror"}}, []byte(`{}`), time.Now())
	st.SetCatalogState(ctx, store.CatalogState{Pass: 1})

	code, body := post(t, srv, "/api/v1/profiles", `{"name":"Partner"}`)
	var partner struct{ ID string }
	json.Unmarshal([]byte(body), &partner)
	if code != 201 {
		t.Fatalf("create: %d %s", code, body)
	}
	for _, bad := range []string{`{"libraries":["lib_` + strings.Repeat("e", 32) + `"]}`, `{"blockedTags":[""]}`, `{"libraries":"x"}`} {
		if code, _ := put(t, srv, "PATCH", "/api/v1/profiles/"+partner.ID, bad); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	code, body = put(t, srv, "PATCH", "/api/v1/profiles/"+partner.ID, `{"libraries":["lib_`+films+`"],"blockedTags":["horror"]}`)
	if code != 200 || !strings.Contains(body, `"libraries":["lib_`+films+`"]`) || !strings.Contains(body, `"blockedTags":["horror"]`) {
		t.Fatalf("limit: %d %s", code, body)
	}
	if code, body := getAs(t, srv, subject, "/api/v1/tags"); code != 200 || body != `{"items":["Horror"]}`+"\n" {
		t.Errorf("tags: %d %s", code, body)
	}
	if code, body := post(t, srv, "/api/v1/profiles/"+partner.ID+"/activate", `{}`); code != 200 {
		t.Fatalf("activate: %d %s", code, body)
	}

	ids := func(path string) ([]string, *int) {
		t.Helper()
		var page struct {
			Items      []struct{ ID string }
			TotalCount *int
		}
		if code := get(t, srv, path, &page); code != 200 {
			t.Fatalf("%s: %d", path, code)
		}
		var out []string
		for _, it := range page.Items {
			out = append(out, strings.TrimLeft(strings.TrimPrefix(it.ID, "title_item_"), "0"))
		}
		return out, page.TotalCount
	}
	// From the index: films 1 to 4, counted by Overfin.
	if got, total := ids("/api/v1/titles?kind=movie"); strings.Join(got, ",") != "1,2,3,4" || total == nil || *total != 4 {
		t.Errorf("index: %v %v", got, total)
	}
	var libs struct{ Items []struct{ ID string } }
	if get(t, srv, "/api/v1/libraries", &libs); len(libs.Items) != 1 || libs.Items[0].ID != "lib_"+films {
		t.Errorf("libraries: %+v", libs)
	}
	if code := get(t, srv, fmt.Sprintf("/api/v1/titles/title_item_%032x", 3), nil); code != 200 {
		t.Errorf("title 3: %d", code)
	}
	for _, id := range []string{"title_movie_tmdb_5", fmt.Sprintf("title_item_%032x", 6)} { // a hidden tag, another library
		if code := get(t, srv, "/api/v1/titles/"+id, nil); code != 404 {
			t.Errorf("%s: %d", id, code)
		}
	}

	// Tags hidden, every library: films no library has show only when
	// their keywords are known and none is hidden (store's TestTitlesTagFree:
	// one only listed stays hidden too).
	post(t, srv, "/api/v1/profiles/"+ownerID(t, srv)+"/activate", `{}`)
	if code, body := put(t, srv, "PATCH", "/api/v1/profiles/"+partner.ID, `{"libraries":null,"blockedTags":["horror"]}`); code != 200 {
		t.Fatalf("tags only: %d %s", code, body)
	}
	now := time.Now()
	st.SetTitlesShown(ctx, "all")
	st.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "100", Name: "Clean"}, []byte(`{}`), now)
	st.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "101", Name: "Scary", Keywords: []string{"HORROR"}}, []byte(`{}`), now)
	for _, id := range []string{"100", "101"} {
		if code := get(t, srv, "/api/v1/titles/title_movie_tmdb_"+id, nil); code != 200 {
			t.Fatalf("film %s, as the owner: %d", id, code)
		}
	}
	post(t, srv, "/api/v1/profiles/"+partner.ID+"/activate", `{}`)
	for id, want := range map[string]int{"100": 200, "101": 404} {
		if code := get(t, srv, "/api/v1/titles/title_movie_tmdb_"+id, nil); code != want {
			t.Errorf("film %s no library has, tags hidden: %d, want %d", id, code, want)
		}
	}

	// Every library again, and no tag: everything.
	post(t, srv, "/api/v1/profiles/"+ownerID(t, srv)+"/activate", `{}`)
	if code, body := put(t, srv, "PATCH", "/api/v1/profiles/"+partner.ID, `{"libraries":null,"blockedTags":null}`); code != 200 ||
		strings.Contains(body, "libraries") || strings.Contains(body, "blockedTags") {
		t.Errorf("lift: %d %s", code, body)
	}
}

// Titles show TMDB's artwork, served only at paths Overfin keeps.
func TestArtworkOnTitles(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{store: &st})
	ctx := context.Background()
	st.PutCatalog(ctx, []store.CatalogItem{
		{ID: fmt.Sprintf("%032x", 3), Kind: "movie", Name: "Heat", SortName: "Heat", TMDB: "949"},
	}, 1)
	st.RefreshTitleKeys(ctx)
	st.SetArtwork(ctx, store.ArtworkTarget{Kind: "movie", TMDB: "949"}, map[string]map[string]string{
		"movie/tmdb/949": {"poster": "/heatposter1.jpg", "logo": "/heatlogo123.png"},
	}, time.Now())

	var it struct {
		Images struct {
			Primary   struct{ URL string }
			Logo      struct{ URL string }
			Backdrops []struct{ URL string }
		}
	}
	if code := get(t, srv, "/api/v1/titles/title_movie_tmdb_949", &it); code != 200 {
		t.Fatalf("title: %d", code)
	}
	if it.Images.Primary.URL != "/api/v1/images/tmdbart_poster_heatposter1_jpg" || it.Images.Logo.URL != "/api/v1/images/tmdbart_logo_heatlogo123_png" {
		t.Errorf("artwork: %+v", it.Images)
	}
	// A path Overfin doesn't keep is refused before TMDB is asked.
	if code := get(t, srv, "/api/v1/images/tmdbart_poster_inventedpath_jpg", nil); code != 404 {
		t.Errorf("invented path: %d", code)
	}
}

// An item the index doesn't have (a song, a film not indexed yet) shows its
// images by its library, asked of Jellyfin once; a profile limited to other
// libraries gets a 404. With tags hidden, a film not indexed yet is hidden
// (its keywords aren't known), a song isn't.
func TestImagesOfItemsOutsideTheIndex(t *testing.T) {
	var st *store.Store
	srv, fake := newServerWith(t, options{store: &st})
	fake.partnerSeesAll = true
	ctx := context.Background()
	films, music := fmt.Sprintf("%032x", 0xf1), fmt.Sprintf("%032x", 0xf2)
	st.PutCatalog(ctx, []store.CatalogItem{{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "1", SortName: "1", LibraryID: films}}, 1)
	st.SetCatalogState(ctx, store.CatalogState{Pass: 1})
	owner := ownerID(t, srv)
	_, body := post(t, srv, "/api/v1/profiles", `{"name":"Partner"}`)
	var partner struct{ ID string }
	json.Unmarshal([]byte(body), &partner)

	song := "/api/v1/images/img_" + fmt.Sprintf("%032x", 7) + "_primary_0_" + unindexedTag
	film := "/api/v1/images/img_" + fmt.Sprintf("%032x", 8) + "_primary_0_" + unindexedTag
	limit := func(patch string) {
		t.Helper()
		post(t, srv, "/api/v1/profiles/"+owner+"/activate", `{}`)
		if code, body := put(t, srv, "PATCH", "/api/v1/profiles/"+partner.ID, patch); code != 200 {
			t.Fatalf("%s: %d %s", patch, code, body)
		}
		post(t, srv, "/api/v1/profiles/"+partner.ID+"/activate", `{}`)
	}
	ancestors := func() int {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.ancestors
	}
	expect := func(path string, want int) {
		t.Helper()
		if code := get(t, srv, path, nil); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}

	// No limit: both, nothing asked of their libraries.
	expect(song, 200)
	expect(film, 200)
	if n := ancestors(); n != 0 {
		t.Errorf("libraries asked with no limit: %d", n)
	}

	// Films only: the song is in music, refused; asked once, remembered.
	limit(`{"libraries":["lib_` + films + `"]}`)
	expect(song, 404)
	expect(song, 404)
	if n := ancestors(); n != 1 {
		t.Errorf("the song's library asked %d times, want once", n)
	}
	// Music only: the song shows, its library not asked again.
	limit(`{"libraries":["lib_` + music + `"]}`)
	expect(song, 200)
	if n := ancestors(); n != 1 {
		t.Errorf("asked again: %d", n)
	}

	// Tags hidden, every library: the film no keyword is known of is
	// hidden, the song isn't.
	limit(`{"libraries":null,"blockedTags":["horror"]}`)
	expect(film, 404)
	expect(song, 200)

	// A tag the item doesn't have, a size that isn't one: 404.
	expect("/api/v1/images/img_"+fmt.Sprintf("%032x", 7)+"_primary_0_"+fmt.Sprintf("%032x", 1), 404)
	expect(song+"?size=huge", 404)
	expect(song+"?width=200", 404)
}
