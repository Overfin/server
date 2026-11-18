package api_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/msfp"
)

// A film's page says where it's available, in the order playing picks
// (the first is Play's): this server by its brand's name, then paired
// servers by the names the menu gives them, each with its picture and
// languages, the paired server's as it sent them (MSFP video section
// 3.2). The control panel's setting off, only the first is given,
// nameless.
func TestTitleAvailability(t *testing.T) {
	ctx := context.Background()
	var stB, stA *store.Store
	var b, a *msfp.Engine
	newServerWith(t, options{store: &stB, federation: &b, fedName: "Bee"})
	stB.PutCatalog(ctx, []store.CatalogItem{{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "Heat", SortName: "heat",
		TMDB: "949", LibraryID: "films", AudioTags: []string{"fr"}, SubtitleTags: []string{"en"}, Height: 2160, HDR: []string{"hdr10"}},
		{ID: fmt.Sprintf("%032x", 2), Kind: "movie", Name: "Up", SortName: "up", TMDB: "14160", LibraryID: "films",
			AudioTags: []string{"en"}, Height: 1080}}, 1)
	stB.RefreshTitleKeys(ctx)
	stB.SetLibraries(ctx, []store.Library{{ID: "films", Item: []byte(`{"Name":"Films","CollectionType":"movies"}`)}})
	srvA, _ := newServerWith(t, options{store: &stA, federation: &a, fedName: "A"})
	stA.PutCatalog(ctx, []store.CatalogItem{{ID: fmt.Sprintf("%032x", 9), Kind: "movie", Name: "Up", SortName: "up",
		TMDB: "14160", LibraryID: "mine", AudioTags: []string{"en"}, Height: 720}}, 1)
	stA.RefreshTitleKeys(ctx)
	stA.SetCatalogState(ctx, store.CatalogState{Pass: 1})
	if err := stA.SetBranding(ctx, true, "Zed"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	stA.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "949", Name: "Heat", Year: 1995}, []byte(`{}`), now)
	stA.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "14160", Name: "Up", Year: 2009}, []byte(`{}`), now)
	aOnB, bOnA := pairEngines(t, b, a)
	if err := b.SetOffers(ctx, aOnB.ID, []string{"films"}); err != nil {
		t.Fatal(err)
	}
	wantAll(t, a, bOnA.ID)

	type source struct {
		Name  string
		Local bool
		Video *struct {
			Height int
			HDR    []string
		}
		Audio, Subtitles []string
	}
	type page struct {
		Sources          []source
		ShowAvailability bool
		Copies           []struct{ Video string }
	}
	names := func(p page) string {
		var out []string
		for _, s := range p.Sources {
			out = append(out, fmt.Sprintf("%s/%v/%d", s.Name, s.Local, s.Video.Height))
		}
		return strings.Join(out, " ")
	}
	// Heat, held by Bee alone: Bee's picture and languages.
	var heat page
	if status := get(t, srvA, "/api/v1/titles/title_movie_tmdb_949", &heat); status != 200 || !heat.ShowAvailability || len(heat.Sources) != 1 {
		t.Fatalf("Heat: %d %+v", status, heat)
	}
	if s := heat.Sources[0]; s.Name != "Bee" || s.Local || s.Video == nil || s.Video.Height != 2160 || !slices.Equal(s.Video.HDR, []string{"hdr10"}) ||
		!slices.Equal(s.Audio, []string{"Français"}) || !slices.Equal(s.Subtitles, []string{"English"}) || len(heat.Copies) != 0 {
		t.Errorf("Heat from Bee: %+v", heat)
	}
	// Up, held by both in the same language: this server first.
	var up page
	get(t, srvA, "/api/v1/titles/title_movie_tmdb_14160", &up)
	if got := names(up); got != "Zed/true/720 Bee/false/1080" {
		t.Errorf("Up: %s", got)
	}
	// In a language only Bee's copy has, Bee first: where Play plays it.
	var me struct {
		AccountID     string
		ActiveProfile struct{ ID string }
		TitlePages    struct{ Availability bool }
	}
	get(t, srvA, "/api/v1/me", &me)
	if !me.TitlePages.Availability {
		t.Error("me: the setting off by default")
	}
	if err := stA.SetAudioLanguage(ctx, me.AccountID, me.ActiveProfile.ID, "de"); err != nil {
		t.Fatal(err)
	}
	stB.PutCatalog(ctx, []store.CatalogItem{{ID: fmt.Sprintf("%032x", 2), Kind: "movie", Name: "Up", SortName: "up", TMDB: "14160",
		LibraryID: "films", AudioTags: []string{"en", "de"}, Height: 1080}}, 1)
	b.CatalogChanged()
	if _, err := a.ReadNow(ctx, bOnA.ID); err != nil {
		t.Fatal(err)
	}
	up = page{}
	get(t, srvA, "/api/v1/titles/title_movie_tmdb_14160", &up)
	if got := names(up); got != "Bee/false/1080 Zed/true/720" {
		t.Errorf("Up in German: %s", got)
	}
	// Off: the first alone, nameless; the media lines still describe it.
	if err := stA.SetTitleAvailability(ctx, false); err != nil {
		t.Fatal(err)
	}
	up = page{}
	get(t, srvA, "/api/v1/titles/title_movie_tmdb_14160", &up)
	if up.ShowAvailability || len(up.Sources) != 1 || up.Sources[0].Name != "" || up.Sources[0].Video.Height != 1080 {
		t.Errorf("off: %+v", up)
	}
	get(t, srvA, "/api/v1/me", &me)
	if me.TitlePages.Availability {
		t.Error("me: the setting still on")
	}
}
