package api_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/msfp-io/msfp-go"
	"github.com/overfin/server/internal/store"
)

// A library no longer wanted, then wanted again before any reading (two
// changes 17 ms apart in the panel): its titles, gone from the site at
// once, come back at the next reading, not only the titles changed since.
func TestPeerLibraryWantedAgainBeforeAReading(t *testing.T) {
	ctx := context.Background()
	var stB, stA *store.Store
	var b, a *msfp.Engine
	newServerWith(t, options{store: &stB, federation: &b, fedName: "Bee"})
	stB.PutCatalog(ctx, []store.CatalogItem{{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "Heat", SortName: "heat",
		TMDB: "949", LibraryID: "films"}}, 1)
	stB.RefreshTitleKeys(ctx)
	putCopy(t, stB, fmt.Sprintf("%032x", 1), "vheatheatheatheatheatheathe", 1)
	stB.SetLibraries(ctx, []store.Library{{ID: "films", Item: []byte(`{"Name":"Films","CollectionType":"movies"}`)}})
	newServerWith(t, options{store: &stA, federation: &a, fedName: "A"})

	aOnB, bOnA := pairEngines(t, b, a)
	if err := b.SetOffers(ctx, aOnB.ID, []string{"films"}); err != nil {
		t.Fatal(err)
	}
	wantAll(t, a, bOnA.ID)
	held := func() int {
		src, err := stA.PeerSources(ctx, "movie/tmdb/949")
		if err != nil {
			t.Fatal(err)
		}
		return len(src)
	}
	if held() != 1 {
		t.Fatal("Heat not read from B")
	}
	wants, _ := a.Wants(ctx, bOnA.ID)
	if err := a.SetWants(ctx, bOnA.ID, nil); err != nil {
		t.Fatal(err)
	}
	if held() != 0 {
		t.Fatal("Heat still listed, its library no longer wanted")
	}
	if err := a.SetWants(ctx, bOnA.ID, []string{wants[0].ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReadNow(ctx, bOnA.ID); err != nil {
		t.Fatal(err)
	}
	if held() != 1 {
		t.Error("Heat not back after its library was wanted again")
	}
}
