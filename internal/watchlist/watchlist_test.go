package watchlist

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/overfin/server/internal/feature"
	"github.com/overfin/server/internal/store"
)

// The valuation reads every profile's watchlist titles, and only titles:
// entries still kept as library items aren't titles yet.
func TestInterests(t *testing.T) {
	ctx := context.Background()
	f := Feature{}
	db, err := store.OpenModule(ctx, "watchlist", filepath.Join(t.TempDir(), "w.db"), f.Info().Migrations)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, row := range [][2]string{{"p1", "title_movie_tmdb_1"}, {"p2", "title_series_tmdb_2_s1"}, {"p2", "item_0123"}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO watchlist (profile_id, item_id, added_at) VALUES (?, ?, 1)`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	var _ feature.Interested = f
	got, err := f.Interests(ctx, db)
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	for _, i := range got {
		if i.TitleID == "item_0123" {
			t.Errorf("an item: %v", got)
		}
	}
}

// A wish that arrived goes on the watchlist once, and not past its cap.
func TestAdd(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenModule(ctx, "watchlist", filepath.Join(t.TempDir(), "w.db"), Feature{}.Info().Migrations)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for range 2 {
		if err := Add(ctx, db, "p1", "title_movie_tmdb_1"); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	db.QueryRowContext(ctx, `SELECT count(*) FROM watchlist WHERE profile_id = 'p1'`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d rows", n)
	}
	for i := range Max {
		db.ExecContext(ctx, `INSERT INTO watchlist (profile_id, item_id, added_at) VALUES ('p2', ?, 1)`, "title_movie_tmdb_"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	Add(ctx, db, "p2", "title_movie_tmdb_new")
	db.QueryRowContext(ctx, `SELECT count(*) FROM watchlist WHERE profile_id = 'p2'`).Scan(&n)
	if n != Max {
		t.Fatalf("past the cap: %d", n)
	}
}
