package api_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/overfin/server/internal/federation"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/store"
)

// Un-offering the library, or ending the pairing, ends a paired server's
// playbacks here at once: their tickets stop working before any request
// comes (MSFP playback, section 6.3).
func TestPeerPlaybacksEndAtOnce(t *testing.T) {
	ctx := context.Background()
	var st *store.Store
	var b *federation.Service
	var onB *playback.Tickets
	srv, fake := newServerWith(t, options{store: &st, federation: &b, tickets: &onB})
	fake.setDirect(true)
	st.PutCatalog(ctx, []store.CatalogItem{
		{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "949", LibraryID: "films"},
	}, 1)
	st.RefreshTitleKeys(ctx)
	st.SetLibraries(ctx, []store.Library{{ID: "films", Item: []byte(`{"Name":"Films"}`)}})
	a, bOnA, aOnB := viewer(t, b, srv.Client())
	play := func() string {
		var s peerSession
		if status, err := a.Call(ctx, bOnA, "/api/v1/federation/play", map[string]any{"title": "movie/tmdb/949", "capabilities": map[string]any{}}, &s); status != 200 {
			t.Fatalf("play: %d %v", status, err)
		}
		live := onB.Where(func(*playback.Stream) bool { return true })
		if len(live) != 1 {
			t.Fatalf("playbacks: %d", len(live))
		}
		return live[0].Ticket
	}
	ticket := play()
	if err := b.SetOffers(ctx, aOnB, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := onB.ByPeerTicket(ticket, time.Now()); ok {
		t.Fatal("the library no longer offered: the ticket still works")
	}
	if err := b.SetOffers(ctx, aOnB, []string{"films"}); err != nil {
		t.Fatal(err)
	}
	ticket = play()
	if err := b.End(ctx, aOnB.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := onB.ByPeerTicket(ticket, time.Now()); ok {
		t.Fatal("the pairing ended: the ticket still works")
	}
}

// The library un-offered while a paired server's playback opens (after
// its request was let in, before it was admitted): it doesn't start.
func TestPeerPlaybackUnofferedWhileOpening(t *testing.T) {
	ctx := context.Background()
	var st *store.Store
	var b *federation.Service
	var onB *playback.Tickets
	srv, fake := newServerWith(t, options{store: &st, federation: &b, tickets: &onB})
	fake.setDirect(true)
	st.PutCatalog(ctx, []store.CatalogItem{
		{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "949", LibraryID: "films"},
	}, 1)
	st.RefreshTitleKeys(ctx)
	st.SetLibraries(ctx, []store.Library{{ID: "films", Item: []byte(`{"Name":"Films"}`)}})
	a, bOnA, aOnB := viewer(t, b, srv.Client())
	fake.mu.Lock()
	fake.onPlaybackInfo = func() {
		if err := b.SetOffers(ctx, aOnB, nil); err != nil {
			t.Error(err)
		}
	}
	fake.mu.Unlock()
	var s peerSession
	if status, _ := a.Call(ctx, bOnA, "/api/v1/federation/play", map[string]any{"title": "movie/tmdb/949", "capabilities": map[string]any{}}, &s); status == 200 {
		t.Errorf("play answered 200")
	}
	if live := onB.Where(func(*playback.Stream) bool { return true }); len(live) != 0 {
		t.Errorf("%d playbacks live", len(live))
	}
}
