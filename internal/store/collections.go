package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Collection is one of TMDB's collections (a series of films, such as a
// saga), as kept: its films by TMDB ID, in release order.
type Collection struct {
	ID       int
	Name     string
	Overview string
	Poster   string // TMDB image paths, checked by the caller
	Backdrop string
	Parts    []string
}

// FilmsToFind returns films whose collection is to be asked of TMDB
// (migration 067's list), limit at most.
func (s *Store) FilmsToFind(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tmdb FROM collections_to_find ORDER BY tmdb LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetFilmCollection keeps the collection a film belongs to (0 for none),
// and takes it off the list of films to find.
func (s *Store) SetFilmCollection(ctx context.Context, tmdb string, collection int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE catalog_titles SET collection = ? WHERE kind = 'movie' AND tmdb = ?`,
		nullInt(collection), tmdb); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM collections_to_find WHERE tmdb = ?`, tmdb); err != nil {
		return err
	}
	return tx.Commit()
}

// CollectionsToFetch returns the collections a library's film belongs to
// that aren't kept, or were fetched before before, limit at most.
func (s *Store) CollectionsToFetch(ctx context.Context, before time.Time, limit int) ([]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT t.collection FROM catalog_titles t
		LEFT JOIN collections c ON c.id = t.collection
		WHERE t.kind = 'movie' AND t.collection IS NOT NULL AND (c.id IS NULL OR c.fetched_at < ?)
			AND EXISTS (SELECT 1 FROM catalog_items i WHERE i.kind = 'movie' AND i.tmdb = t.tmdb)
		ORDER BY t.collection LIMIT ?`, before.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetCollection keeps a collection fetched at at, and its films as titles
// (refreshed, keeping their pages).
func (s *Store) SetCollection(ctx context.Context, c Collection, films []Title, at time.Time) error {
	if err := s.PutTitles(ctx, films, at); err != nil {
		return err
	}
	parts, err := json.Marshal(c.Parts)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO collections (id, name, overview, poster, backdrop, parts, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET name = excluded.name, overview = excluded.overview, poster = excluded.poster,
			backdrop = excluded.backdrop, parts = excluded.parts, fetched_at = excluded.fetched_at`,
		c.ID, c.Name, c.Overview, nullString(c.Poster), nullString(c.Backdrop), string(parts), at.Unix())
	return err
}

// FilmsToDescribe returns those of films (TMDB IDs) whose page isn't kept,
// or was fetched before before.
func (s *Store) FilmsToDescribe(ctx context.Context, films []string, before time.Time) ([]string, error) {
	if len(films) == 0 {
		return nil, nil
	}
	args := []any{before.Unix()}
	for _, f := range films {
		args = append(args, f)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT tmdb FROM catalog_titles WHERE kind = 'movie'
		AND (details_at IS NULL OR details_at < ?) AND tmdb IN (`+marks(len(films))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// PurgeCollections deletes the collections not fetched again since before.
func (s *Store) PurgeCollections(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM collections WHERE fetched_at < ?`, before.Unix())
	return err
}

// Collections returns every collection kept that a library's film belongs
// to, by name.
func (s *Store) Collections(ctx context.Context) ([]Collection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, overview, poster, backdrop, parts FROM collections c
		WHERE EXISTS (SELECT 1 FROM catalog_titles t JOIN catalog_items i ON i.kind = 'movie' AND i.tmdb = t.tmdb
			WHERE t.kind = 'movie' AND t.collection = c.id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Collection
	for rows.Next() {
		c, err := scanCollection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortCollections(out)
	return out, nil
}

// CollectionByID returns a kept collection, nil when there's none.
func (s *Store) CollectionByID(ctx context.Context, id int) (*Collection, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, overview, poster, backdrop, parts FROM collections WHERE id = ?`, id)
	c, err := scanCollection(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// FilmCollection returns the kept collection a film belongs to, nil when
// none is kept.
func (s *Store) FilmCollection(ctx context.Context, tmdb string) (*Collection, error) {
	var id sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT collection FROM catalog_titles WHERE kind = 'movie' AND tmdb = ?`, tmdb).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !id.Valid) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.CollectionByID(ctx, int(id.Int64))
}

func scanCollection(row interface{ Scan(...any) error }) (Collection, error) {
	var c Collection
	var poster, backdrop sql.NullString
	var parts string
	if err := row.Scan(&c.ID, &c.Name, &c.Overview, &poster, &backdrop, &parts); err != nil {
		return c, err
	}
	c.Poster, c.Backdrop = poster.String, backdrop.String
	// Kept by Overfin: a bad list is an empty one.
	_ = json.Unmarshal([]byte(parts), &c.Parts)
	for i, p := range c.Parts {
		if _, err := strconv.Atoi(p); err != nil {
			c.Parts = c.Parts[:i]
			break
		}
	}
	return c, nil
}

// sortCollections sorts collections by name, as titles are sorted, the
// word "collection" or "saga" around it aside: TMDB names most of them
// "Collection Avatar" in French and "Avatar Collection" in English.
func sortCollections(cs []Collection) {
	keys := make(map[int]string, len(cs))
	for _, c := range cs {
		keys[c.ID] = SortName(collectionSortName(c.Name))
	}
	slices.SortFunc(cs, func(a, b Collection) int {
		if c := strings.Compare(keys[a.ID], keys[b.ID]); c != 0 {
			return c
		}
		return a.ID - b.ID
	})
}

// collectionWords are what names a collection as one, around its name.
var collectionWords = []string{"la collection", "la saga", "collection", "saga"}

// collectionSortName is a collection's name without the words naming it
// one, at its start or its end; the name itself when nothing else is left.
func collectionSortName(name string) string {
	n := strings.TrimSpace(name)
	// The words are ASCII: compared byte for byte, whatever the name.
	for _, w := range collectionWords {
		if len(n) > len(w) && n[len(w)] == ' ' && strings.EqualFold(n[:len(w)], w) {
			n = strings.TrimSpace(n[len(w):])
			break
		}
	}
	for _, w := range collectionWords {
		if at := len(n) - len(w); at > 0 && n[at-1] == ' ' && strings.EqualFold(n[at:], w) {
			n = strings.TrimRight(n[:at], " -–:")
			break
		}
	}
	if n == "" {
		return name
	}
	return n
}

// CollectionItem is a library's copy of one of a collection's films.
type CollectionItem struct {
	Collection int
	TMDB       string // the film's
	ItemID     string // the copy's
}

// CollectionItems returns the library's copies of collections' films, for
// collections (all of them when nil).
func (s *Store) CollectionItems(ctx context.Context, collections []int) ([]CollectionItem, error) {
	q := `SELECT t.collection, t.tmdb, i.id FROM catalog_titles t
		JOIN catalog_items i ON i.kind = 'movie' AND i.tmdb = t.tmdb
		WHERE t.kind = 'movie' AND t.collection IS NOT NULL`
	var args []any
	if collections != nil {
		if len(collections) == 0 {
			return nil, nil
		}
		q += ` AND t.collection IN (` + marks(len(collections)) + `)`
		for _, c := range collections {
			args = append(args, c)
		}
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY t.collection, t.tmdb, i.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CollectionItem
	for rows.Next() {
		var c CollectionItem
		if err := rows.Scan(&c.Collection, &c.TMDB, &c.ItemID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ImageCollections returns the kept collections whose poster or backdrop
// (typ) is at path.
func (s *Store) ImageCollections(ctx context.Context, typ, path string) ([]int, error) {
	var column string
	switch typ {
	case "poster":
		column = "poster"
	case "backdrop":
		column = "backdrop"
	default:
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM collections WHERE `+column+` = ? ORDER BY id LIMIT 50`, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// AddFilmToFind lists a film whose collection is to be asked of TMDB, as
// migration 067 lists them (for tests).
func (s *Store) AddFilmToFind(ctx context.Context, tmdb string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO collections_to_find (tmdb) VALUES (?)`, tmdb)
	return err
}
