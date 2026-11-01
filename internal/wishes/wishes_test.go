package wishes

import (
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

var now = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// wishlist builds wishes: account, profile, then its titles in order.
func wishlist(lists ...[]string) []store.Wish {
	var out []store.Wish
	for _, l := range lists {
		for i, t := range l[2:] {
			out = append(out, store.Wish{AccountID: l[0], ProfileID: l[1], Title: t, Position: i + 1})
		}
	}
	return out
}

func titles(order []Next) []string {
	var out []string
	for _, n := range order {
		out = append(out, n.AccountID+":"+n.Title)
	}
	return out
}

func same(t *testing.T, got []Next, want ...string) {
	t.Helper()
	g := titles(got)
	if len(g) != len(want) {
		t.Fatalf("got %v, want %v", g, want)
	}
	for i := range g {
		if g[i] != want[i] {
			t.Fatalf("got %v, want %v", g, want)
		}
	}
}

// Accounts take turns, round after round, however long their lists; the
// one whose turn is oldest goes first.
func TestAccountsTakeTurns(t *testing.T) {
	w := wishlist([]string{"a", "a1", "m1", "m2", "m3"}, []string{"b", "b1", "m4"}, []string{"c", "c1", "m5", "m6"})
	same(t, Order(w, nil, nil, now, 10), "a:m1", "b:m4", "c:m5", "a:m2", "c:m6", "a:m3")
	turns := map[string]store.WishTurn{"a": {At: now}, "b": {At: now.Add(-time.Hour)}}
	same(t, Order(w, nil, turns, now, 3), "c:m5", "b:m4", "a:m1")
}

// An account's profiles take turns inside its own: more profiles don't
// mean more turns. The profile after the one whose wish went last starts.
func TestProfilesTakeTurnsInsideTheirAccount(t *testing.T) {
	w := wishlist([]string{"a", "p1", "m1", "m2"}, []string{"a", "p2", "m3", "m4"}, []string{"b", "q1", "m5", "m6"})
	same(t, Order(w, nil, nil, now, 10), "a:m1", "b:m5", "a:m3", "b:m6", "a:m2", "a:m4")
	turns := map[string]store.WishTurn{"a": {At: now.Add(-time.Hour), ProfileID: "p1"}, "b": {At: now}}
	if got := Order(w, nil, turns, now, 1); got[0].ProfileID != "p2" || got[0].Title != "m3" {
		t.Fatalf("after p1's turn: %+v", got)
	}
}

// A wish that can't be tried now keeps its place, and the next one goes:
// one not found until it's due again, one downloading or not out yet not
// at all.
func TestStuckWishesAreSkipped(t *testing.T) {
	w := wishlist([]string{"a", "p", "m1", "m2", "m3", "m4", "m5"})
	states := map[string]store.WishState{
		"m1": {State: store.WishNotFound, NextTry: now.Add(time.Hour)},
		"m2": {State: store.WishDownloading},
		"m3": {State: store.WishUnreleased, ReleaseOn: "2027-01-01"},
		"m4": {State: store.WishNotFound, NextTry: now.Add(-time.Minute)},
	}
	same(t, Order(w, states, nil, now, 10), "a:m4", "a:m5")
}

// A title several people wished is fetched once, on the first turn that
// reaches it.
func TestSharedWishGoesOnce(t *testing.T) {
	w := wishlist([]string{"a", "p", "m1", "m2"}, []string{"b", "q", "m1", "m3"})
	same(t, Order(w, nil, nil, now, 10), "a:m1", "b:m3", "a:m2")
}

func TestLimit(t *testing.T) {
	w := wishlist([]string{"a", "p", "m1", "m2"}, []string{"b", "q", "m3"})
	same(t, Order(w, nil, nil, now, 2), "a:m1", "b:m3")
	if got := Order(nil, nil, nil, now, 5); len(got) != 0 {
		t.Fatalf("nothing wished: %v", got)
	}
}
