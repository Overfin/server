package store

import (
	"context"
	"strings"
	"time"
)

// Search (POST /search).

// FillSearchNames gives the titles kept before searching existed their
// search name, once.
func (s *Store) FillSearchNames(ctx context.Context) error {
	rows, err := s.read.QueryContext(ctx, `SELECT kind, tmdb, name FROM catalog_titles WHERE search_name IS NULL`)
	if err != nil {
		return err
	}
	type row struct{ kind, tmdb, name string }
	var todo []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.kind, &r.tmdb, &r.name); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(todo) == 0 {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range todo {
		if _, err := tx.ExecContext(ctx, `UPDATE catalog_titles SET search_name = ? WHERE kind = ? AND tmdb = ?`,
			Fold(r.name), r.kind, r.tmdb); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// likeWords is a LIKE pattern for a folded query: its words, in order,
// anything between them.
func likeWords(query string) string {
	var b strings.Builder
	b.WriteByte('%')
	for _, w := range strings.Fields(query) {
		b.WriteString(strings.TrimSuffix(likePrefix(w), "%"))
		b.WriteByte('%')
	}
	return b.String()
}

// SearchLibrary finds the library's films and series (by title key) whose
// name matches query: TMDB's name, or a file's own for one TMDB doesn't
// know, or a name an admin gave it, in any language. Whole names first,
// then names that start so, then the others, the best rated first. At
// most limit.
func (s *Store) SearchLibrary(ctx context.Context, query string, limit int) ([]string, error) {
	q := Fold(query)
	if q == "" {
		return nil, nil
	}
	pattern, prefix := likeWords(q), strings.TrimSuffix(likePrefix(q), "%")+"%"
	name := `coalesce(t.search_name, lower(i.name))`
	rank := func(name string) string {
		return `CASE WHEN ` + name + ` = ?1 THEN 0 WHEN ` + name + ` LIKE ?2 ESCAPE '\' THEN 1 WHEN ` + name + ` LIKE ?3 ESCAPE '\' THEN 2 ELSE 3 END`
	}
	edited := `(SELECT min(` + rank("e.folded") + `) FROM title_edits e
		WHERE e.kind = i.kind AND e.tmdb = i.tmdb AND e.field = 'name')`
	// TMDB's names and ratings from their index (see fromTMDB).
	return s.ids(ctx, `SELECT title_key FROM (
			SELECT title_key, min(rank) AS rank, max(rating) AS rating FROM (
				SELECT i.title_key, min(`+rank(name)+`, coalesce(`+edited+`, 3)) AS rank,
					coalesce(t.rating, i.community_rating, 0) AS rating
				FROM catalog_items i LEFT JOIN catalog_titles t INDEXED BY catalog_titles_listing ON t.kind = i.kind AND t.tmdb = i.tmdb
				WHERE i.kind IN ('movie', 'series') AND i.title_key IS NOT NULL)
			WHERE rank < 3 GROUP BY title_key)
		ORDER BY rank, rating DESC, title_key LIMIT ?4`, q, prefix, pattern, limit)
}

// PersonHit is a person a search found.
type PersonHit struct {
	TMDB, Name, Profile, Department string
}

// PutPeopleHits keeps the people a search found, so their photos can be
// shown, without their pages (fetched when opened); those kept already
// stay as they are.
func (s *Store) PutPeopleHits(ctx context.Context, people []PersonHit, at time.Time) error {
	for _, p := range people {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO tmdb_people (id, name, biography, birthplace, department, profile,
				credits, fetched_at) VALUES (?, ?, '', '', ?, ?, '[]', ?) ON CONFLICT (id) DO NOTHING`,
			p.TMDB, p.Name, p.Department, nullString(p.Profile), at.Unix()); err != nil {
			return err
		}
	}
	return nil
}
