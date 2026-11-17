package federation

import (
	"context"
	"strings"
	"testing"

	"github.com/overfin/server/internal/store"
)

// pairedWithFilms is A, holding Heat in its films (Jellyfin's ID "films"),
// offered to B, paired.
func pairedWithFilms(t *testing.T) (a, b *server, bOnA, aOnB *store.Peer) {
	t.Helper()
	ctx := context.Background()
	a, b = newServer(t, "A"), newServer(t, "B")
	libraries(t, a, `{"Id":"films","Name":"Films","CollectionType":"movies"}`)
	a.s.Store.PutCatalog(ctx, []store.CatalogItem{{ID: "00000000000000000000000000000001", Kind: "movie", Name: "Heat",
		SortName: "heat", TMDB: "949", LibraryID: "films"}}, 1)
	a.s.Store.RefreshTitleKeys(ctx)
	code, _, _ := a.s.NewCode(ctx, "")
	aOnB, err := b.s.UseCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	bOnA, _ = a.s.Accept(ctx, onlyPeer(t, a).ID)
	aOnB, _ = b.s.Store.PeerByID(ctx, aOnB.ID)
	if err := a.s.SetOffers(ctx, bOnA, []string{"films"}); err != nil {
		t.Fatal(err)
	}
	return a, b, bOnA, aOnB
}

// A's libraries reach B by IDs of A's own, never Jellyfin's (MSFP section
// 9.3), in libraries and titles alike; asking for Jellyfin's ID gets
// nothing.
func TestLibraryIDsOpaque(t *testing.T) {
	ctx := context.Background()
	a, b, _, aOnB := pairedWithFilms(t)
	theirs, err := b.s.TheirLibraries(ctx, aOnB)
	if err != nil || len(theirs) != 1 || theirs[0].ID == "films" || strings.Contains(theirs[0].ID, "films") ||
		theirs[0].ID != pub(t, a, "films")[0] || theirs[0].Name != "Films" {
		t.Fatalf("B sees: %+v, %v", theirs, err)
	}
	var answer titlesAnswer
	if _, err := b.s.Call(ctx, aOnB, "/api/v1/federation/titles", titlesRequest{Libraries: []string{"films"}}, &answer); err != nil || len(answer.Titles) != 0 {
		t.Fatalf("by Jellyfin's ID: %+v, %v", answer, err)
	}
	if err := b.s.SetWants(ctx, aOnB, []string{theirs[0].ID}); err != nil {
		t.Fatal(err)
	}
	if n, err := b.s.SyncTitles(ctx, aOnB); err != nil || n != 1 {
		t.Fatalf("read: %d, %v", n, err)
	}
	got, _ := b.s.Store.TitlesOfPeer(ctx, aOnB.ID)
	if len(got) != 1 || got[0].Library != theirs[0].ID {
		t.Fatalf("B keeps: %+v", got)
	}
}

// A peer whose library IDs all changed (an Overfin moving to opaque IDs):
// B's wants name IDs no longer offered. Its next reading drops what it
// knew of them, marks them gone for its operator to choose again, and
// lists nothing of them in the menu; choosing again reads the titles back.
func TestLibraryIDsChanged(t *testing.T) {
	ctx := context.Background()
	a, b, _, aOnB := pairedWithFilms(t)
	// What B kept from A before: A's Jellyfin ID, and its titles under it.
	b.s.Store.SetPeerWants(ctx, aOnB.ID, []store.PeerLibrary{{ID: "films", Name: "Films", Kind: "movies"}})
	b.s.Store.SetPeerTitles(ctx, aOnB.ID, []store.PeerTitle{{Key: "movie/tmdb/949", Kind: "movie", Library: "films"},
		{Key: "movie/tmdb/1", Kind: "movie", Library: "films"}})
	b.s.Store.SetPeerTitlesRead(ctx, aOnB.ID, 1, 1)
	if menu, _ := b.s.Store.MenuLibraries(ctx); len(menu) != 1 {
		t.Fatalf("menu before: %+v", menu)
	}
	if _, err := b.s.SyncTitles(ctx, aOnB); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.s.Store.TitlesOfPeer(ctx, aOnB.ID); len(got) != 0 {
		t.Fatalf("ghosts left: %+v", got)
	}
	if menu, _ := b.s.Store.MenuLibraries(ctx); len(menu) != 0 {
		t.Fatalf("menu after: %+v", menu)
	}
	if w, _ := b.s.Store.PeerWants(ctx, aOnB.ID); len(w) != 1 || !w[0].Gone {
		t.Fatalf("wants: %+v", w)
	}
	// B's operator chooses again.
	if err := b.s.SetWants(ctx, aOnB, pub(t, a, "films")); err != nil {
		t.Fatal(err)
	}
	if n, err := b.s.SyncTitles(ctx, aOnB); err != nil || n != 1 {
		t.Fatalf("read again: %d, %v", n, err)
	}
	if w, _ := b.s.Store.PeerWants(ctx, aOnB.ID); len(w) != 1 || w[0].Gone || w[0].ID != pub(t, a, "films")[0] {
		t.Fatalf("wants again: %+v", w)
	}
}

// Saving the same offers again (the panel saves them with each edit of a
// pairing) changes nothing: the peer's next reading isn't a whole one.
func TestSameOffersChangeNothing(t *testing.T) {
	ctx := context.Background()
	a, _, bOnA, _ := pairedPair(t)
	before, err := a.s.Store.PeerOffersSeq(ctx, bOnA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.s.SetOffers(ctx, bOnA, []string{"films", "films"}); err != nil {
		t.Fatal(err)
	}
	if after, _ := a.s.Store.PeerOffersSeq(ctx, bOnA.ID); after != before {
		t.Errorf("offers' number moved from %d to %d", before, after)
	}
	if err := a.s.SetOffers(ctx, bOnA, nil); err != nil {
		t.Fatal(err)
	}
	if after, _ := a.s.Store.PeerOffersSeq(ctx, bOnA.ID); after == before {
		t.Errorf("a real change didn't move it")
	}
}
