package api_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/overfin/server/internal/api"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

// Open requests become wishes once, on their profiles' wishlists: a film,
// the seasons asked for, a whole series as its seasons out now; declined
// ones don't. Then requests and Seerr users' links are forgotten.
func TestCopyRequests(t *testing.T) {
	ctx := context.Background()
	box, _ := seal.New(make([]byte, 32))
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	acct, p, _ := st.CreateAccount(ctx, "subject-1", store.NewOwner{Name: "Ann"})
	st.SetSeerrUser(ctx, acct.ID, 42, time.Now())
	det, _ := json.Marshal(store.TitleDetails{Seasons: []store.SeasonInfo{{Number: 0, AirDate: "2010-01-01"}, {Number: 1, AirDate: "2011-04-17"}, {Number: 2, AirDate: "2099-01-01"}}})
	st.SetTitleDetails(ctx, store.Title{Kind: "series", TMDB: "1399", Name: "GoT"}, det, time.Now())
	at := time.Now().Add(-time.Hour)
	for i, r := range []store.Request{
		{Title: "movie/tmdb/1", Status: "pending"},
		{Title: "series/tmdb/5", Seasons: []int{3, 2}, Status: "approved"},
		{Title: "series/tmdb/1399", Status: "processing"},
		{Title: "movie/tmdb/9", Status: "declined"},
	} {
		r.AccountID, r.ProfileID, r.SeerrID, r.CreatedAt = acct.ID, p.ID, i+1, at.Add(time.Duration(i)*time.Minute)
		if err := st.AddRequest(ctx, &r); err != nil {
			t.Fatal(err)
		}
	}
	if err := api.CopyRequests(ctx, st); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Wishes(ctx, p.ID)
	var titles []string
	for _, w := range got {
		titles = append(titles, w.Title)
	}
	want := []string{"movie/tmdb/1", "series/tmdb/5/s2", "series/tmdb/5/s3", "series/tmdb/1399/s1"}
	if !slices.Equal(titles, want) {
		t.Fatalf("wishes %v, want %v", titles, want)
	}
	if left, _ := st.OpenRequests(ctx); len(left) != 0 {
		t.Errorf("requests left: %+v", left)
	}
	if _, ok, _ := st.SeerrUser(ctx, acct.ID); ok {
		t.Error("the Seerr user's link is kept")
	}
	// Once: a second start copies nothing.
	st.RemoveWish(ctx, p.ID, "movie/tmdb/1")
	if err := api.CopyRequests(ctx, st); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Wishes(ctx, p.ID); len(got) != 3 {
		t.Errorf("copied twice: %+v", got)
	}
}
