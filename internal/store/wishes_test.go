package store

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"
)

func wishTitles(ws []Wish) []string {
	var out []string
	for _, w := range ws {
		out = append(out, w.Title)
	}
	return out
}

// A wishlist keeps the person's order: added at the end, moved, removed;
// a title already on it stays where it is.
func TestWishlistOrder(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, p, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	for _, title := range []string{"movie/tmdb/1", "series/tmdb/2", "series/tmdb/3/s2", "movie/tmdb/1"} {
		if err := s.AddWish(ctx, p.ID, title); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.Wishes(ctx, p.ID)
	if !slices.Equal(wishTitles(got), []string{"movie/tmdb/1", "series/tmdb/2", "series/tmdb/3/s2"}) || got[2].Position != 3 {
		t.Fatalf("added: %+v", got)
	}
	if err := s.OrderWishes(ctx, p.ID, []string{"series/tmdb/3/s2", "movie/tmdb/1", "series/tmdb/2"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]string{{"movie/tmdb/1"}, {"movie/tmdb/1", "movie/tmdb/1", "series/tmdb/2"}, {"movie/tmdb/9", "movie/tmdb/1", "series/tmdb/2"}} {
		if err := s.OrderWishes(ctx, p.ID, bad); !errors.Is(err, ErrWishOrder) {
			t.Errorf("order %v: %v", bad, err)
		}
	}
	s.RemoveWish(ctx, p.ID, "movie/tmdb/1")
	got, _ = s.Wishes(ctx, p.ID)
	if !slices.Equal(wishTitles(got), []string{"series/tmdb/3/s2", "series/tmdb/2"}) || got[1].Position != 2 {
		t.Fatalf("after a removal: %+v", got)
	}
}

func TestWishlistFull(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, p, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	for i := range MaxWishes {
		if err := s.AddWish(ctx, p.ID, "movie/tmdb/"+strconv.Itoa(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddWish(ctx, p.ID, "movie/tmdb/99999"); !errors.Is(err, ErrWishlistFull) {
		t.Fatalf("one too many: %v", err)
	}
	if err := s.AddWish(ctx, p.ID, "movie/tmdb/1"); err != nil {
		t.Fatalf("one already there: %v", err)
	}
}

// An arrived title leaves every wishlist it was on, into each profile's
// arrivals, told once; its state goes.
func TestWishArrives(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, a, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	_, b, _ := s.CreateAccount(ctx, "subject-2", owner("2"))
	s.AddWish(ctx, a.ID, "movie/tmdb/1")
	s.AddWish(ctx, a.ID, "movie/tmdb/2")
	s.AddWish(ctx, b.ID, "movie/tmdb/2")
	s.SetWishState(ctx, WishState{Title: "movie/tmdb/2", State: WishDownloading})
	profiles, err := s.ArriveWish(ctx, "movie/tmdb/2")
	slices.Sort(profiles)
	want := []string{a.ID, b.ID}
	slices.Sort(want)
	if err != nil || !slices.Equal(profiles, want) {
		t.Fatalf("arrived for %v, %v", profiles, err)
	}
	if got, _ := s.Wishes(ctx, a.ID); !slices.Equal(wishTitles(got), []string{"movie/tmdb/1"}) || got[0].Position != 1 {
		t.Fatalf("a's wishlist: %+v", got)
	}
	if states, _ := s.WishStates(ctx); len(states) != 0 {
		t.Fatalf("states: %v", states)
	}
	if got, _ := s.UnseenArrivals(ctx, b.ID); !slices.Equal(got, []string{"movie/tmdb/2"}) {
		t.Fatalf("b told: %v", got)
	}
	s.SeeArrivals(ctx, b.ID)
	if got, _ := s.UnseenArrivals(ctx, b.ID); len(got) != 0 {
		t.Fatalf("told twice: %v", got)
	}
	if got, _ := s.Arrivals(ctx, time.Now().Add(-time.Hour)); len(got) != 2 {
		t.Fatalf("arrivals for the score: %+v", got)
	}
}

// A profile whose wishing is off keeps its wishes, out of the turns.
func TestWishesOff(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	acct, p, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	s.AddWish(ctx, p.ID, "movie/tmdb/1")
	s.SetWishesOff(ctx, p.ID, true)
	if off, _ := s.WishesOff(ctx, p.ID); !off {
		t.Fatal("not off")
	}
	if all, _ := s.AllWishes(ctx); len(all) != 0 {
		t.Fatalf("in the turns while off: %+v", all)
	}
	s.SetWishesOff(ctx, p.ID, false)
	all, _ := s.AllWishes(ctx)
	if len(all) != 1 || all[0].AccountID != acct.ID {
		t.Fatalf("back on: %+v", all)
	}
	s.SpendWishTurn(ctx, acct.ID, p.ID)
	if turns, _ := s.WishTurns(ctx); turns[acct.ID].ProfileID != p.ID || time.Since(turns[acct.ID].At) > time.Minute {
		t.Fatalf("turns: %+v", turns)
	}
}
