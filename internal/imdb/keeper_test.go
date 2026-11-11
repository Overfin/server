package imdb

import (
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeIMDb serves the test datasets, gzipped, as IMDb does; broken makes
// it answer 503.
type fakeIMDb struct {
	mu      sync.Mutex
	broken  bool
	ratings string
	asked   []string
}

func (f *fakeIMDb) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, r.URL.Path)
	if f.broken {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	body := map[string]string{"/" + BasicsFile: basics, "/" + EpisodesFile: episodes, "/" + RatingsFile: f.ratings}[r.URL.Path]
	if body == "" {
		http.NotFound(w, r)
		return
	}
	z := gzip.NewWriter(w)
	io.WriteString(z, body)
	z.Close()
}

func TestKeeper(t *testing.T) {
	fake := &fakeIMDb{ratings: ratings}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	dir := filepath.Join(t.TempDir(), "imdb")
	var on bool
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	k := NewKeeper(dir, func(context.Context) (bool, error) { return on, nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	k.BaseURL, k.Client, k.least, k.now = srv.URL+"/", srv.Client(), 1, func() time.Time { return now }
	ctx := context.Background()

	// Off: nothing's downloaded.
	if err := k.Once(ctx); err != nil || k.Data() != nil || len(fake.asked) != 0 {
		t.Fatalf("off: %v %v %v", err, k.Data(), fake.asked)
	}
	// On: the three datasets, made into the file; they don't stay.
	on = true
	if err := k.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if m, ok := k.Data().Title("tt0133093"); !ok || m.Rating != 8.7 {
		t.Fatalf("The Matrix: %+v %v", m, ok)
	}
	if s := k.Status(); s.Titles != 7 || !s.Built.Equal(now) || s.Problem != "" || s.Working {
		t.Errorf("status: %+v", s)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 || entries[0].Name() != fileName || entries[1].Name() != "votes-2026-11-07" {
		t.Errorf("left in the folder: %v", entries)
	}
	// Within a day, nothing's downloaded again.
	asked := len(fake.asked)
	now = now.Add(23 * time.Hour)
	k.Once(ctx)
	if len(fake.asked) != asked {
		t.Errorf("downloaded again within a day: %v", fake.asked[asked:])
	}

	// A day on, IMDb is down: the file in use stays, the problem is said,
	// and the next try waits.
	now = now.Add(2 * time.Hour)
	fake.broken = true
	if err := k.Once(ctx); err == nil {
		t.Error("no error")
	}
	if _, ok := k.Data().Title("tt0133093"); !ok {
		t.Error("the file in use went")
	}
	if s := k.Status(); s.Problem == "" || s.Titles != 7 {
		t.Errorf("status: %+v", s)
	}
	asked = len(fake.asked)
	now = now.Add(time.Hour)
	k.Once(ctx)
	if len(fake.asked) != asked {
		t.Error("tried again at once")
	}

	// Back, with a new rating: the new file takes the old one's place.
	fake.broken = false
	fake.ratings = "tconst\taverageRating\tnumVotes\ntt0133093\t9.1\t2300000\n"
	now = now.Add(retryAfter)
	if err := k.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if m, _ := k.Data().Title("tt0133093"); m.Rating != 9.1 {
		t.Errorf("after the new download: %+v", m)
	}
	if days, _ := VoteDays(dir); len(days) != 2 {
		t.Errorf("days of votes: %v", days)
	}
	if s := k.Status(); s.Problem != "" {
		t.Errorf("status: %+v", s)
	}

	// A new keeper (Overfin restarted) opens the file there.
	k2 := NewKeeper(dir, func(context.Context) (bool, error) { return true, nil }, k.Logger)
	k2.BaseURL, k2.Client, k2.now = srv.URL+"/", srv.Client(), k.now
	asked = len(fake.asked)
	if err := k2.Once(ctx); err != nil || k2.Data() == nil || len(fake.asked) != asked {
		t.Errorf("after a restart: %v %v %v", err, k2.Data(), fake.asked[asked:])
	}
	k2.close()

	// Off: the file goes, and the folder with it.
	d := k.Data()
	on = false
	if err := k.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if k.Data() != nil {
		t.Error("still in use")
	}
	if _, ok := d.Title("tt0133093"); ok {
		t.Error("the old file still answers")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the folder stays: %v", err)
	}
}

// Too few titles: IMDb sent something else, and the file in use stays.
func TestKeeperRefusesTooFewTitles(t *testing.T) {
	srv := httptest.NewServer(&fakeIMDb{ratings: ratings})
	defer srv.Close()
	k := NewKeeper(t.TempDir(), func(context.Context) (bool, error) { return true, nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	k.BaseURL, k.Client = srv.URL+"/", srv.Client()
	if err := k.Once(context.Background()); err == nil || k.Data() != nil {
		t.Errorf("7 titles taken: %v", err)
	}
}

// Too few ratings: no day of votes is kept, which would rank nothing, or
// count a title's whole votes as gained.
func TestKeeperRefusesTooFewRatings(t *testing.T) {
	srv := httptest.NewServer(&fakeIMDb{ratings: ratings})
	defer srv.Close()
	dir := t.TempDir()
	k := NewKeeper(dir, func(context.Context) (bool, error) { return true, nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	k.BaseURL, k.Client = srv.URL+"/", srv.Client()
	if err := k.Once(context.Background()); err == nil {
		t.Fatal("taken")
	}
	if days, err := VoteDays(dir); err != nil || len(days) != 0 {
		t.Errorf("days kept: %v, %v", days, err)
	}
}

// A dataset that inflates past the limit is refused, not read to the end.
func TestInflatedIsCapped(t *testing.T) {
	c := &capped{r: strings.NewReader(strings.Repeat("x", 100)), left: 10}
	b, err := io.ReadAll(c)
	if len(b) != 10 || err == nil {
		t.Fatalf("read %d, %v", len(b), err)
	}
}
