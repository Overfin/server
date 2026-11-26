package tmdb

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"
)

// The streaming and at-home picks' queries: a country's providers in the
// order TMDB shows them there (those without a place there last), at most
// n; the discover queries as they go out; and inputs that can't be a
// query refused before anything is asked of TMDB.
func TestPicksQueries(t *testing.T) {
	var mu sync.Mutex
	var asked []url.URL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, *r.URL)
		mu.Unlock()
		switch r.URL.Path {
		case "/watch/providers/movie", "/watch/providers/tv":
			io.WriteString(w, `{"results":[
				{"provider_id":8,"display_priorities":{"CA":2,"US":1}},
				{"provider_id":337,"display_priorities":{"US":3}},
				{"provider_id":230,"display_priorities":{"CA":1}},
				{"provider_id":0,"display_priorities":{"CA":0}},
				{"provider_id":119,"display_priorities":{"CA":5}}]}`)
		default:
			io.WriteString(w, `{"page":1,"total_pages":1,"results":[]}`)
		}
	}))
	defer srv.Close()
	c, err := New(srv.URL, testToken)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	got, err := c.Providers(ctx, "movie", "CA", 3)
	if err != nil || !slices.Equal(got, []int{230, 8}) {
		// The ID 0 (no provider) is placed first in CA and takes one of the
		// three places before it is dropped; then 230 and 8.
		t.Errorf("providers: %v %v", got, err)
	}
	if got, _ := c.Providers(ctx, "series", "CA", 10); !slices.Equal(got, []int{230, 8, 119, 337}) {
		t.Errorf("all providers, those without a place in CA last: %v", got)
	}
	if mu.Lock(); asked[len(asked)-1].Path != "/watch/providers/tv" || asked[len(asked)-1].Query().Get("watch_region") != "CA" {
		t.Errorf("series providers asked: %v", asked[len(asked)-1])
	}
	mu.Unlock()

	since := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if _, err := c.Streaming(ctx, "movie", "CA", []int{230, 8}, since, 2); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	q := asked[len(asked)-1]
	mu.Unlock()
	v := q.Query()
	if q.Path != "/discover/movie" || v.Get("with_watch_providers") != "230|8" || v.Get("watch_region") != "CA" ||
		v.Get("primary_release_date.gte") != "2026-10-01" || v.Get("with_watch_monetization_types") != "flatrate" || v.Get("page") != "2" {
		t.Errorf("streaming films: %s", q.String())
	}
	c.Streaming(ctx, "series", "FR", []int{8}, since, 1)
	mu.Lock()
	q = asked[len(asked)-1]
	mu.Unlock()
	if q.Path != "/discover/tv" || q.Query().Get("air_date.gte") != "2026-10-01" || q.Query().Has("primary_release_date.gte") {
		t.Errorf("streaming series: %s", q.String())
	}
	c.OutAtHome(ctx, "US", since, 1)
	mu.Lock()
	q = asked[len(asked)-1]
	mu.Unlock()
	if q.Path != "/discover/movie" || q.Query().Get("with_release_type") != "4|5|6" || q.Query().Get("region") != "US" ||
		q.Query().Get("release_date.gte") != "2026-10-01" || q.Query().Get("release_date.lte") == "" {
		t.Errorf("out at home: %s", q.String())
	}

	mu.Lock()
	before := len(asked)
	mu.Unlock()
	bad := []func() error{
		func() error { _, err := c.Providers(ctx, "movie", "ca", 3); return err },
		func() error { _, err := c.Providers(ctx, "movie", "CAN", 3); return err },
		func() error { _, err := c.Providers(ctx, "book", "CA", 3); return err },
		func() error { _, err := c.Providers(ctx, "movie", "CA", 0); return err },
		func() error { _, err := c.Streaming(ctx, "movie", "CA", nil, since, 1); return err },
		func() error { _, err := c.Streaming(ctx, "movie", "CA", []int{8}, since, 0); return err },
		func() error { _, err := c.Streaming(ctx, "movie", "CA", []int{8}, since, 501); return err },
		func() error { _, err := c.Streaming(ctx, "movie", "É1", []int{8}, since, 1); return err },
		func() error { _, err := c.OutAtHome(ctx, "", since, 1); return err },
		func() error { _, err := c.OutAtHome(ctx, "US", since, 501); return err },
	}
	for i, f := range bad {
		if f() == nil {
			t.Errorf("bad query %d: no error", i)
		}
	}
	mu.Lock()
	if len(asked) != before {
		t.Errorf("bad queries reached TMDB: %v", asked[before:])
	}
	mu.Unlock()
}
