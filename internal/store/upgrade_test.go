package store

import (
	"bytes"
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/overfin/server/internal/seal"
)

// openAt opens a new database with the migrations up to version only, as
// an older Overfin left it.
func openAt(t *testing.T, version int) *Store {
	t.Helper()
	box, err := seal.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "old.db")+"?_pragma=foreign_keys(1)&_pragma=temp_store(2)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	s := &Store{db: db, read: db, box: box, now: time.Now}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	for i, name := range names[:version] {
		body, err := migrations.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.apply(context.Background(), name, string(body), i+1); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// oldTitleDetails keeps a title's page as older Overfins did, before the
// columns SetTitleDetails writes now.
func oldTitleDetails(t *testing.T, s *Store, title Title, details string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := s.PutTitles(ctx, []Title{title}, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE catalog_titles SET details = ?, details_at = ? WHERE kind = ? AND tmdb = ?`,
		details, at.Unix(), title.Kind, title.TMDB); err != nil {
		t.Fatal(err)
	}
}

// Migrations 049 to 058 on a database in use (0.5.0's, at 048) keep TMDB's
// data: titles' pages, seasons and their episodes, artwork. They're
// fetched again in time, and the keeper's purges that follow delete none
// of it.
func TestUpgradeFrom048KeepsData(t *testing.T) {
	ctx := context.Background()
	s := openAt(t, 48)
	now := time.Now()
	fetched := now.Add(-24 * time.Hour)
	oldTitleDetails(t, s, Title{Kind: "series", TMDB: "1399", Name: "Game of Thrones"}, `{"language":"en"}`, fetched)
	if err := s.SetSeason(ctx, "1399", Season{Number: 1, Name: "Season 1", Episodes: []Episode{{Number: 1, Name: "Winter Is Coming", Still: "/a.jpg"}}}, fetched); err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO artwork (key, type, path) VALUES ('series/tmdb/1399', 'poster', '/p.jpg')`, nil},
		{`INSERT INTO artwork_fetched (key, fetched_at) VALUES ('series/tmdb/1399', ?)`, []any{fetched.Unix()}},
		{`INSERT INTO title_ratings (kind, tmdb, country, certification) VALUES ('series', '1399', 'US', 'TV-MA')`, nil},
		{`INSERT INTO title_ratings_fetched (kind, tmdb, fetched_at) VALUES ('series', '1399', ?)`, []any{fetched.Unix()}},
	} {
		if _, err := s.db.ExecContext(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshTitleAges(ctx); err != nil {
		t.Fatal(err)
	}
	// What the keeper does each pass: purge what wasn't fetched again in
	// five months.
	before := now.AddDate(0, -5, 0)
	if err := s.PurgeSeasons(ctx, before); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeArtwork(ctx, before); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PurgeTitles(ctx, before); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeRatings(ctx, before); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]int{
		`SELECT count(*) FROM catalog_titles WHERE details IS NOT NULL`: 1,
		`SELECT count(*) FROM catalog_seasons`:                          1,
		`SELECT count(*) FROM catalog_episodes`:                         1,
		`SELECT count(*) FROM artwork`:                                  1,
		`SELECT count(*) FROM title_ratings`:                            1,
		`SELECT count(*) FROM title_ages WHERE age = 17`:                1,
		// Fetched again: the title's page and its artwork.
		`SELECT count(*) FROM catalog_titles WHERE details_at = 0`: 1,
		`SELECT count(*) FROM artwork_fetched`:                     0,
	} {
		var n int
		if err := s.db.QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Errorf("%s: %d, want %d", q, n, want)
		}
	}
}

// Migration 067 lists the films whose page was kept, to find their
// collection: not series, nor films whose page never was.
func TestUpgradeListsFilmsToFind(t *testing.T) {
	ctx := context.Background()
	s := openAt(t, 66)
	now := time.Now()
	oldTitleDetails(t, s, Title{Kind: "movie", TMDB: "11", Name: "Star Wars"}, `{}`, now)
	oldTitleDetails(t, s, Title{Kind: "series", TMDB: "1399", Name: "Game of Thrones"}, `{}`, now)
	if err := s.PutTitles(ctx, []Title{{Kind: "movie", TMDB: "12", Name: "Never opened"}}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := s.FilmsToFind(ctx, 10); err != nil || len(got) != 1 || got[0] != "11" {
		t.Errorf("films to find: %v %v", got, err)
	}
	// Its page fetched again: its collection known, off the list.
	if err := s.SetTitleDetails(ctx, Title{Kind: "movie", TMDB: "11", Name: "Star Wars", Collection: 10}, []byte(`{}`), now); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FilmsToFind(ctx, 10); len(got) != 0 {
		t.Errorf("after its page: %v", got)
	}
}
