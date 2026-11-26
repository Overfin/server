package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// Titles kept before search existed get their search name once, folded
// as search folds what's typed; those that have one keep it.
func TestFillSearchNames(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if err := s.PutTitles(ctx, []Title{{Kind: "movie", TMDB: "1", Name: "Le Fabuleux Destin d'Amélie Poulain"}, {Kind: "series", TMDB: "2", Name: "Dark"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	s.db.ExecContext(ctx, `UPDATE catalog_titles SET search_name = NULL WHERE tmdb = '1'`)
	s.db.ExecContext(ctx, `UPDATE catalog_titles SET search_name = 'kept as it was' WHERE tmdb = '2'`)
	if err := s.FillSearchNames(ctx); err != nil {
		t.Fatal(err)
	}
	name := func(tmdb string) string {
		var n sql.NullString
		s.db.QueryRowContext(ctx, `SELECT search_name FROM catalog_titles WHERE tmdb = ?`, tmdb).Scan(&n)
		return n.String
	}
	if got := name("1"); got != Fold("Le Fabuleux Destin d'Amélie Poulain") || got == "" {
		t.Errorf("filled: %q", got)
	}
	if got := name("2"); got != "kept as it was" {
		t.Errorf("one with a name changed: %q", got)
	}
	if err := s.FillSearchNames(ctx); err != nil {
		t.Errorf("again, nothing to do: %v", err)
	}
}
