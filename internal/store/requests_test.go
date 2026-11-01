package store

import (
	"context"
	"slices"
	"testing"
	"time"
)

// The requests from before wishlists: the open ones are listed for the
// copy onto wishlists, then forgotten, with the Seerr users' links, once.
func TestRequests(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	acct, prof, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	s.SetSeerrUser(ctx, acct.ID, 42, time.Now())
	for i, status := range []string{"pending", "declined", "processing", "available"} {
		r := Request{AccountID: acct.ID, ProfileID: prof.ID, Title: "series/tmdb/1399", Seasons: []int{i + 1}, SeerrID: i + 1,
			Status: status, CreatedAt: time.Now().Add(time.Duration(i) * time.Minute)}
		if err := s.AddRequest(ctx, &r); err != nil {
			t.Fatal(err)
		}
	}
	open, err := s.OpenRequests(ctx)
	if err != nil || len(open) != 2 || !slices.Equal(open[0].Seasons, []int{1}) || open[1].Status != "processing" {
		t.Fatalf("open: %+v %v", open, err)
	}
	if done, _ := s.RequestsCopied(ctx); done {
		t.Fatal("copied before")
	}
	if err := s.ForgetRequests(ctx); err != nil {
		t.Fatal(err)
	}
	if open, _ := s.OpenRequests(ctx); len(open) != 0 {
		t.Errorf("left: %+v", open)
	}
	if _, ok, _ := s.SeerrUser(ctx, acct.ID); ok {
		t.Error("a Seerr user's link left")
	}
	if done, _ := s.RequestsCopied(ctx); !done {
		t.Error("not marked copied")
	}
}
