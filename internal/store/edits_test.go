package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// A title's edits show wherever it does, in their language: texts in
// theirs only, artwork in theirs first, then the one for every language;
// a series' name and images on its seasons and episodes too. Giving a
// field back to TMDB shows TMDB's again.
func TestTitleEdits(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, err := s.SetLanguages(ctx, []string{"fr-CA", "en-CA"}, "fr-CA"); err != nil {
		t.Fatal(err)
	}
	s.PutTitles(ctx, []Title{{Kind: "movie", TMDB: "550988", Name: "L'homme libre", Overview: "TMDB's", Poster: "/tmdbposter1.jpg"},
		{Kind: "series", TMDB: "1399", Name: "Le Trône de fer", Poster: "/gotposter1.jpg", Backdrop: "/gotbackdrop.jpg"}}, time.Now())
	at := time.Now()
	for _, e := range []struct{ kind, tmdb, field, language, value string }{
		{"movie", "550988", "name", "fr-CA", "Free Guy"},
		{"movie", "550988", "overview", "en-CA", "Mine"},
		{"movie", "550988", "poster", AllLanguages, "/allposter12.jpg"},
		{"movie", "550988", "poster", "en-CA", "/enposter123.jpg"},
		{"series", "1399", "name", "fr-CA", "GoT"},
		{"series", "1399", "backdrop", AllLanguages, "/mybackdrop1.jpg"},
	} {
		if err := s.SetTitleEdit(ctx, e.kind, e.tmdb, e.field, e.language, e.value, at); err != nil {
			t.Fatalf("%v: %v", e, err)
		}
	}
	keys := []string{"movie/tmdb/550988", "series/tmdb/1399", "series/tmdb/1399/s1", "series/tmdb/1399/s1/e2"}
	fr, err := s.TitleInfos(ctx, keys, "2026-10-31", "")
	if err != nil {
		t.Fatal(err)
	}
	if m := fr["movie/tmdb/550988"]; m.Name != "Free Guy" || m.Overview != "TMDB's" || m.Poster != "/allposter12.jpg" {
		t.Errorf("film, French: %+v", m)
	}
	if se := fr["series/tmdb/1399"]; se.Name != "GoT" || se.Backdrop != "/mybackdrop1.jpg" || se.Poster != "/gotposter1.jpg" {
		t.Errorf("series: %+v", se)
	}
	for _, k := range keys[2:] {
		if x := fr[k]; x.SeriesName != "GoT" || x.Backdrop != "/mybackdrop1.jpg" {
			t.Errorf("%s: %q %q", k, x.SeriesName, x.Backdrop)
		}
	}
	en, err := s.TitleInfos(ctx, keys[:1], "2026-10-31", "en-CA")
	if err != nil {
		t.Fatal(err)
	}
	// The French name stays French: English has TMDB's (untranslated, the
	// default language's).
	if m := en["movie/tmdb/550988"]; m.Name != "L'homme libre" || m.Overview != "Mine" || m.Poster != "/enposter123.jpg" {
		t.Errorf("film, English: %+v", m)
	}
	if got, _ := s.ImageTitles(ctx, "poster", "/enposter123.jpg", 10); !slices.Equal(got, []string{"movie/tmdb/550988"}) {
		t.Errorf("image titles: %v", got)
	}
	if art, _ := s.EditedArtwork(ctx); !slices.Equal(art["poster"], []string{"/allposter12.jpg", "/enposter123.jpg"}) {
		t.Errorf("edited artwork: %v", art)
	}
	if err := s.DeleteTitleEdit(ctx, "movie", "550988", "name", "fr-CA"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTitleEdit(ctx, "movie", "550988", "name", "fr-CA"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted twice: %v", err)
	}
	if fr, _ := s.TitleInfos(ctx, keys[:1], "2026-10-31", ""); fr["movie/tmdb/550988"].Name != "L'homme libre" {
		t.Errorf("back to TMDB's: %+v", fr["movie/tmdb/550988"])
	}
	// TMDB's page fetched again leaves the edits.
	s.PutTitles(ctx, []Title{{Kind: "series", TMDB: "1399", Name: "Game of Thrones"}}, time.Now())
	if fr, _ := s.TitleInfos(ctx, keys[1:2], "2026-10-31", ""); fr["series/tmdb/1399"].Name != "GoT" {
		t.Errorf("after TMDB again: %+v", fr["series/tmdb/1399"])
	}
}

// Only the edits Overfin keeps are taken.
func TestTitleEditChecked(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for _, e := range []struct{ kind, tmdb, field, language, value string }{
		{"episode", "1", "name", "fr-CA", "x"},
		{"movie", "abc", "name", "fr-CA", "x"},
		{"movie", "1", "year", "fr-CA", "2020"},
		{"movie", "1", "name", AllLanguages, "x"},
		{"movie", "1", "name", "", "x"},
		{"movie", "1", "name", "fr-CA", "  "},
		{"movie", "1", "name", "fr-CA", strings.Repeat("é", MaxEditedName+1)},
		{"movie", "1", "poster", "fr-CA", "https://example.org/a.jpg"},
		{"movie", "1", "poster", "fr-CA", "/../../etc/passwd.jpg"},
		{"movie", "1", "poster", "fr-CA", UploadValue(strings.Repeat("a", 32))}, // not uploaded
	} {
		if err := s.SetTitleEdit(ctx, e.kind, e.tmdb, e.field, e.language, e.value, time.Now()); !errors.Is(err, ErrInvalidEdit) {
			t.Errorf("%v: %v", e, err)
		}
	}
}

// An upload is the artwork of the titles whose edits use it, of its own
// type only, and goes once none does (after a while: one just uploaded
// waits to be chosen).
func TestTitleUploads(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, other := strings.Repeat("a", 32), strings.Repeat("b", 32)
	long := time.Now().Add(-2 * uploadUnused)
	put := func(x, typ string) {
		t.Helper()
		if err := s.PutTitleUpload(ctx, x, TitleUpload{Type: typ, ContentType: "image/jpeg", Data: []byte("jpeg")}, long); err != nil {
			t.Fatal(err)
		}
	}
	put(id, "poster")
	if err := s.SetTitleEdit(ctx, "movie", "1", "backdrop", AllLanguages, UploadValue(id), time.Now()); !errors.Is(err, ErrInvalidEdit) {
		t.Errorf("a poster as a backdrop: %v", err)
	}
	if err := s.SetTitleEdit(ctx, "movie", "1", "poster", AllLanguages, UploadValue(id), time.Now()); err != nil {
		t.Fatal(err)
	}
	if keys, _ := s.UploadTitles(ctx, id); !slices.Equal(keys, []string{"movie/tmdb/1"}) {
		t.Errorf("upload's titles: %v", keys)
	}
	// Another, never chosen, goes with the next upload or edit; the one
	// chosen stays.
	put(other, "poster")
	put(strings.Repeat("d", 32), "logo")
	if _, ok, _ := s.TitleUploadOf(ctx, other); ok {
		t.Error("an unused upload stayed")
	}
	if _, ok, _ := s.TitleUploadOf(ctx, id); !ok {
		t.Error("the upload chosen went")
	}
	if err := s.SetTitleEdit(ctx, "movie", "1", "poster", AllLanguages, "/tmdbposter1.jpg", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.TitleUploadOf(ctx, id); ok {
		t.Error("a replaced upload stayed")
	}
	// One just uploaded stays until it's chosen.
	fresh := strings.Repeat("c", 32)
	s.PutTitleUpload(ctx, fresh, TitleUpload{Type: "logo", ContentType: "image/png", Data: []byte("png")}, time.Now())
	s.DeleteTitleEdit(ctx, "movie", "1", "poster", AllLanguages)
	if _, ok, _ := s.TitleUploadOf(ctx, fresh); !ok {
		t.Error("a fresh upload went")
	}
}

// The library is searched by the names admins gave too, in any language,
// and still by TMDB's.
func TestSearchEditedNames(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "movie", Name: "x", SortName: "x", TMDB: "1"},
		{ID: id(1), Kind: "movie", Name: "y", SortName: "y", TMDB: "2"},
	}, 1)
	s.RefreshTitleKeys(ctx)
	s.PutTitles(ctx, []Title{{Kind: "movie", TMDB: "1", Name: "Évasion"}, {Kind: "movie", TMDB: "2", Name: "Autre chose"}}, time.Now())
	s.SetTitleEdit(ctx, "movie", "2", "name", "en-CA", "The Great Évasion", time.Now())
	if got, _ := s.SearchLibrary(ctx, "evasion", 10); !slices.Equal(got, []string{"movie/tmdb/1", "movie/tmdb/2"}) {
		t.Errorf("évasion: %v", got)
	}
	if got, _ := s.SearchLibrary(ctx, "autre", 10); !slices.Equal(got, []string{"movie/tmdb/2"}) {
		t.Errorf("TMDB's name: %v", got)
	}
}
