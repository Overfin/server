package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
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

	to := config.WishesToOvertide
	s := &api.WishesToSeerr{Store: st, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Setup: func() (string, int, *seerr.Client) { return to, 2, client }}
	// Wishes go to Overtide: nothing goes to Seerr.
	if err := s.Pass(ctx); err != nil || len(fake.requests) != 0 || len(fake.users) != 0 {
		t.Fatalf("to Overtide: %v, %d requests", err, len(fake.requests))
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

// fakeSeerrRequests is Seerr for the wishes handed to it: users, requests
// and their statuses, as Seerr keeps them.
type fakeSeerrRequests struct {
	mu       sync.Mutex
	users    map[int]map[string]any
	requests map[int]map[string]any
	next     int
}

func (f *fakeSeerrRequests) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	switch {
	case r.Method == "POST" && r.URL.Path == "/api/v1/user":
		f.next++
		f.users[f.next] = body
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"id":%d}`, f.next)
	case strings.HasPrefix(r.URL.Path, "/api/v1/user/"):
		var id int
		fmt.Sscan(strings.TrimPrefix(r.URL.Path, "/api/v1/user/"), &id)
		user := f.users[id]
		switch {
		case user == nil:
			w.WriteHeader(http.StatusNotFound)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/settings/main"):
			json.NewEncoder(w).Encode(map[string]any{"username": user["username"], "email": user["email"], "locale": "fr"})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/settings/main"):
			if body["email"] != user["email"] || body["locale"] != "fr" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			user["username"] = body["username"]
		default:
			json.NewEncoder(w).Encode(map[string]any{"id": id, "username": user["username"]})
		}
	case r.Method == "POST" && r.URL.Path == "/api/v1/request":
		// As Seerr does: the requester is the user the header names; the
		// API key alone would be its admin.
		var as float64
		fmt.Sscan(r.Header.Get("X-Api-User"), &as)
		if as == 0 || body["userId"] != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		body["userId"] = as
		for _, x := range f.requests {
			if x["mediaId"] == body["mediaId"] && x["userId"] == body["userId"] {
				w.WriteHeader(http.StatusConflict)
				return
			}
		}
		f.next++
		body["status"] = 1
		f.requests[f.next] = body
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": f.next, "status": 1, "type": body["mediaType"],
			"media": map[string]any{"tmdbId": body["mediaId"], "status": 2}})
	case r.Method == "GET" && r.URL.Path == "/api/v1/request":
		var out []map[string]any
		for id, x := range f.requests {
			if by := r.URL.Query().Get("requestedBy"); by == "" || fmt.Sprint(x["userId"]) == by {
				out = append(out, map[string]any{"id": id, "status": x["status"], "type": x["mediaType"],
					"media": map[string]any{"tmdbId": x["mediaId"], "status": 2}})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"results": out})
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/v1/request/"):
		var id int
		fmt.Sscan(strings.TrimPrefix(r.URL.Path, "/api/v1/request/"), &id)
		x := f.requests[id]
		if x == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		media := 2
		if m, ok := x["mediaStatus"].(int); ok {
			media = m
		}
		json.NewEncoder(w).Encode(map[string]any{"id": id, "status": x["status"], "type": x["mediaType"],
			"media": map[string]any{"tmdbId": x["mediaId"], "status": media}})
	case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/v1/request/"):
		var id int
		fmt.Sscan(strings.TrimPrefix(r.URL.Path, "/api/v1/request/"), &id)
		if x := f.requests[id]; x != nil && fmt.Sprint(x["userId"]) != r.Header.Get("X-Api-User") {
			w.WriteHeader(http.StatusForbidden) // someone else's
			return
		}
		delete(f.requests, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNotFound)
	}
}
