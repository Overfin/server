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
