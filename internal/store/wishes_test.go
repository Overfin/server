package store

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
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
	for _, bad := range [][]string{{"movie/tmdb/1", "movie/tmdb/1", "series/tmdb/2"}, {"movie/tmdb/9", "movie/tmdb/1", "series/tmdb/2"}} {
		if err := s.OrderWishes(ctx, p.ID, bad); !errors.Is(err, ErrWishOrder) {
			t.Errorf("order %v: %v", bad, err)
		}
	}
	// Some not named (the profile can't see them now): they keep their
	// places, the others share theirs. Order is s2, 1, 2; naming 2 then s2
	// leaves 1 in the middle.
	if err := s.OrderWishes(ctx, p.ID, []string{"series/tmdb/2", "series/tmdb/3/s2"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Wishes(ctx, p.ID); !slices.Equal(wishTitles(got), []string{"series/tmdb/2", "movie/tmdb/1", "series/tmdb/3/s2"}) {
		t.Errorf("with one not named: %v", wishTitles(got))
	}
	s.RemoveWish(ctx, p.ID, "movie/tmdb/1")
	got, _ = s.Wishes(ctx, p.ID)
	if !slices.Equal(wishTitles(got), []string{"series/tmdb/2", "series/tmdb/3/s2"}) || got[1].Position != 2 {
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

// Wishes weigh like requests: what's on a wishlist, and what arrived less
// than the kept days ago for a profile that hasn't watched it yet.
func TestWishesInTheValuation(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	acct, p, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	s.AddWish(ctx, p.ID, "movie/tmdb/1")
	s.AddWish(ctx, p.ID, "movie/tmdb/2")
	s.AddWish(ctx, p.ID, "movie/tmdb/3")
	s.ArriveWish(ctx, "movie/tmdb/2")
	s.ArriveWish(ctx, "movie/tmdb/3")
	s.RecordPlay(ctx, p.ID, "movie/tmdb/3", strings.Repeat("0", 32), 0, true, time.Now())
	requested := func(keptDays int) []string {
		t.Helper()
		f, err := s.ValuationFacts(ctx, time.Now(), nil, keptDays)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range f.Requests {
			if r.AccountID != acct.ID {
				t.Errorf("another account: %+v", r)
			}
			out = append(out, r.Title)
		}
		slices.Sort(out)
		return out
	}
	// The wish waiting, and the one that arrived unwatched; not the one
	// watched.
	if got := requested(60); !slices.Equal(got, []string{"movie/tmdb/1", "movie/tmdb/2"}) {
		t.Errorf("kept 60 days: %v", got)
	}
	// Once the kept days are over, an arrival weighs no more.
	if got := requested(0); !slices.Equal(got, []string{"movie/tmdb/1"}) {
		t.Errorf("not kept: %v", got)
	}
}

// A series' seasons follow each other, where its first one is, by season
// number; films keep their place.
func TestGroupedWishes(t *testing.T) {
	got := GroupedWishes([]string{"series/tmdb/1/s2", "movie/tmdb/9", "series/tmdb/5/s3", "series/tmdb/1/s1", "series/tmdb/5/s1"})
	want := []string{"series/tmdb/1/s1", "series/tmdb/1/s2", "movie/tmdb/9", "series/tmdb/5/s1", "series/tmdb/5/s3"}
	if !slices.Equal(got, want) {
		t.Fatalf("%v", got)
	}
}

// A title several accounts wished for spends each one's turn when it's
// fetched: an account once, on its profile whose wish is highest.
func TestSpendWishTurns(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	ann, a1, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	a2, err := s.AddProfile(ctx, ann.ID, NewProfile{Name: "Second", Kind: "standard"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	bob, b1, _ := s.CreateAccount(ctx, "subject-2", owner("2"))
	_, c1, _ := s.CreateAccount(ctx, "subject-3", owner("3"))
	s.AddWish(ctx, a1.ID, "movie/tmdb/9")
	s.AddWish(ctx, a1.ID, "movie/tmdb/1")
	s.AddWish(ctx, a2.ID, "movie/tmdb/1")
	s.AddWish(ctx, b1.ID, "movie/tmdb/1")
	s.AddWish(ctx, c1.ID, "movie/tmdb/2")
	n, err := s.SpendWishTurns(ctx, "movie/tmdb/1", WishTurnOf{})
	if err != nil || n != 2 {
		t.Fatalf("%d accounts, %v", n, err)
	}
	turns, _ := s.WishTurns(ctx)
	if len(turns) != 2 || turns[ann.ID].ProfileID != a2.ID || turns[bob.ID].ProfileID != b1.ID {
		t.Fatalf("turns: %+v", turns)
	}
	// Fetched on the first profile's turn: that turn is recorded as the
	// first profile's, whatever the other's wish's place, so the account's
	// next turn goes to the other profile.
	s.AddWish(ctx, a1.ID, "movie/tmdb/3")
	s.AddWish(ctx, a2.ID, "movie/tmdb/3")
	if _, err := s.SpendWishTurns(ctx, "movie/tmdb/3", WishTurnOf{AccountID: ann.ID, ProfileID: a1.ID}); err != nil {
		t.Fatal(err)
	}
	if turns, _ := s.WishTurns(ctx); turns[ann.ID].ProfileID != a1.ID {
		t.Errorf("the turn's profile: %+v", turns[ann.ID])
	}
}

// Where wishes go, noted: a change drops every state the last fetcher left;
// the first time, only those the other fetcher alone gives.
func TestWishesGoTo(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	set := func(states ...WishState) {
		for _, st := range states {
			if err := s.SetWishState(ctx, st); err != nil {
				t.Fatal(err)
			}
		}
	}
	titles := func() []string {
		states, _ := s.WishStates(ctx)
		var out []string
		for k := range states {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	set(WishState{Title: "movie/tmdb/1", State: WishRequested, SeerrRequest: 7},
		WishState{Title: "movie/tmdb/2", State: WishRequested},
		WishState{Title: "movie/tmdb/3", State: WishDownloading, SeerrRequest: 8},
		WishState{Title: "movie/tmdb/4", State: WishDownloading},
		WishState{Title: "movie/tmdb/5", State: WishUnreleased, ReleaseOn: "2027-01-01"})
	// Nothing noted yet, to Overtide: Seerr's go.
	if dropped, err := s.WishesGoTo(ctx, "overtide"); err != nil || !dropped {
		t.Fatalf("first time: %v %v", dropped, err)
	}
	if got := titles(); !slices.Equal(got, []string{"movie/tmdb/4", "movie/tmdb/5"}) {
		t.Errorf("the first time, to Overtide: %v", got)
	}
	// The same again, or no wishes at all: nothing changes.
	if dropped, _ := s.WishesGoTo(ctx, "overtide"); dropped {
		t.Error("dropped with no change")
	}
	if dropped, _ := s.WishesGoTo(ctx, ""); dropped || len(titles()) != 2 {
		t.Error("dropped with wishes off")
	}
	// To Seerr: everything Overtide left waits again.
	if dropped, err := s.WishesGoTo(ctx, "seerr"); err != nil || !dropped || len(titles()) != 0 {
		t.Errorf("to Seerr: %v %v %v", dropped, err, titles())
	}
}

// The first time, to Seerr: only Overtide's downloading goes.
func TestWishesGoToSeerrFirst(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	s.SetWishState(ctx, WishState{Title: "movie/tmdb/1", State: WishRequested, SeerrRequest: 7})
	s.SetWishState(ctx, WishState{Title: "movie/tmdb/2", State: WishDownloading, SeerrRequest: 8})
	s.SetWishState(ctx, WishState{Title: "movie/tmdb/3", State: WishDownloading})
	if _, err := s.WishesGoTo(ctx, "seerr"); err != nil {
		t.Fatal(err)
	}
	states, _ := s.WishStates(ctx)
	if len(states) != 2 || states["movie/tmdb/3"].State != "" {
		t.Errorf("states: %+v", states)
	}
}
