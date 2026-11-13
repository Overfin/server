package store

import (
	"context"
	"testing"
	"time"
)

// Episodes without a frame are listed once each, by their first file;
// one with a frame isn't, one none could be taken of is again after the
// retry time, and one that needs none (SkipFrame) only after its date.
func TestFrames(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for _, it := range []struct{ id, key string }{
		{"a2", "series/tmdb/1/s1/e1"}, {"a1", "series/tmdb/1/s1/e1"}, {"b1", "series/tmdb/1/s1/e2"},
		{"c1", "series/tmdb/1/s1/e3"}, {"d1", "series/tmdb/1/s1/e4"},
	} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO catalog_items (id, kind, name, sort_name, seen, rating, title_key, runtime_ms)
			VALUES (?, 'episode', 'E', 'e', 1, '', ?, 1800000)`, it.id, it.key); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Unix(1_800_000_000, 0)
	keys := func(at time.Time) []string {
		cs, err := s.FrameCandidates(ctx, at.Add(-24*time.Hour), 10)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, c := range cs {
			out = append(out, c.TitleKey[len(c.TitleKey)-2:]+":"+c.Item)
			if c.Runtime != 30*time.Minute {
				t.Errorf("runtime %v", c.Runtime)
			}
		}
		return out
	}
	if got := keys(now); !equal(got, []string{"e1:a1", "e2:b1", "e3:c1", "e4:d1"}) {
		t.Fatalf("candidates %v", got)
	}
	const id = "0123456789abcdef0123456789abcdef"
	if err := s.PutFrame(ctx, "series/tmdb/1/s1/e1", "a1", id, []byte("jpeg"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.PutFrame(ctx, "series/tmdb/1/s1/e2", "b1", "", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := s.SkipFrame(ctx, "series/tmdb/1/s1/e3", "c1", now.Add(30*24*time.Hour), 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if got := keys(now); !equal(got, []string{"e4:d1"}) {
		t.Errorf("right after: %v", got)
	}
	if got := keys(now.Add(25 * time.Hour)); !equal(got, []string{"e2:b1", "e4:d1"}) {
		t.Errorf("a day later: %v", got)
	}
	if got := keys(now.Add(31 * 24 * time.Hour)); !equal(got, []string{"e2:b1", "e3:c1", "e4:d1"}) {
		t.Errorf("a month later: %v", got)
	}
	data, key, ok, err := s.Frame(ctx, id)
	if err != nil || !ok || string(data) != "jpeg" || key != "series/tmdb/1/s1/e1" {
		t.Errorf("Frame: %q %q %v %v", data, key, ok, err)
	}
	if f, err := s.frameOf(ctx, "series/tmdb/1/s1/e1"); err != nil || f != id {
		t.Errorf("frameOf: %q %v", f, err)
	}
	if f, _ := s.frameOf(ctx, "series/tmdb/1/s1/e2"); f != "" {
		t.Errorf("no frame taken, yet %q", f)
	}
	if err := s.PutFrame(ctx, "k", "i", "not-hex", []byte("x"), now); err == nil {
		t.Error("a bad ID was taken")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
