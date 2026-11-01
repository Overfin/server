package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/overfin/server/internal/api"
	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/seerr"
	"github.com/overfin/server/internal/store"
)

// With Seerr as where wishes go, Overfin requests them there itself, in
// its turns, at most so many a day, as one Seerr user of its own; each
// send spends a turn; what Seerr does with a request is read back.
func TestWishesToSeerr(t *testing.T) {
	ctx := context.Background()
	fake := &fakeSeerrRequests{users: map[int]map[string]any{}, requests: map[int]map[string]any{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	client, _ := seerr.New(srv.URL, "key")
	box, _ := seal.New(make([]byte, 32))
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, ann, _ := st.CreateAccount(ctx, "subject-ann", store.NewOwner{Name: "Ann"})
	_, bob, _ := st.CreateAccount(ctx, "subject-bob", store.NewOwner{Name: "Bob"})
	st.AddWish(ctx, ann.ID, "movie/tmdb/1")
	st.AddWish(ctx, ann.ID, "movie/tmdb/2")
	st.AddWish(ctx, bob.ID, "series/tmdb/1399/s2")

	to := config.WishesToOverswell
	s := &api.WishesToSeerr{Store: st, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Setup: func() (string, int, *seerr.Client) { return to, 2, client }}
	// Wishes go to Overswell: nothing goes to Seerr.
	if err := s.Pass(ctx); err != nil || len(fake.requests) != 0 || len(fake.users) != 0 {
		t.Fatalf("to Overswell: %v, %d requests", err, len(fake.requests))
	}
	to = config.WishesToSeerr
	if err := s.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	// Two a day: each account's first wish, as one user named Overfin.
	if len(fake.users) != 1 || fake.users[1]["username"] != "Overfin" || len(fake.requests) != 2 {
		t.Fatalf("users %v, requests %v", fake.users, fake.requests)
	}
	states, _ := st.WishStates(ctx)
	if states["movie/tmdb/1"].State != store.WishRequested || states["movie/tmdb/1"].SeerrRequest == 0 ||
		states["series/tmdb/1399/s2"].State != store.WishRequested {
		t.Fatalf("states: %+v", states)
	}
	if turns, _ := st.WishTurns(ctx); len(turns) != 2 {
		t.Fatalf("turns spent: %+v", turns)
	}
	// The day's count is used: film 2 waits.
	if err := s.Pass(ctx); err != nil || len(fake.requests) != 2 {
		t.Fatalf("past the day's count: %v, %d requests", err, len(fake.requests))
	}
	// Seerr declines film 1, and starts the season: read back.
	fake.mu.Lock()
	for _, x := range fake.requests {
		if x["mediaType"] == "movie" {
			x["status"] = 3
		} else {
			x["status"], x["mediaStatus"] = 2, 3
		}
	}
	fake.mu.Unlock()
	if err := s.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	states, _ = st.WishStates(ctx)
	if f := states["movie/tmdb/1"]; f.State != store.WishNotFound || f.SeerrRequest != 0 || time.Until(f.NextTry) < 29*24*time.Hour {
		t.Errorf("declined: %+v", f)
	}
	if s2 := states["series/tmdb/1399/s2"]; s2.State != store.WishDownloading {
		t.Errorf("started: %+v", s2)
	}
}
