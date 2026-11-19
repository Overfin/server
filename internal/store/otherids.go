package store

import (
	"context"
	"database/sql"
	"errors"
)

// KeysByOtherID returns the keys of the titles this server keeps with an
// IMDb ("imdb", tt0133093) or TheTVDB ("tvdb", 81189) ID, up to five: the
// films and series TMDB gave it with their pages (the libraries', the
// paired servers', those someone opened), then the libraries' own titles
// as Jellyfin has them, then the episodes TMDB gave it. None when it's
// kept with no title: TMDB is to be asked.
func (s *Store) KeysByOtherID(ctx context.Context, source, id string) ([]string, error) {
	var q string
	switch source {
	case "imdb":
		q = `SELECT kind || '/tmdb/' || tmdb, 1 FROM catalog_titles WHERE imdb = ?1
			UNION ALL SELECT title_key, 2 FROM catalog_items WHERE imdb = ?1 AND title_key IS NOT NULL
			UNION ALL SELECT 'series/tmdb/' || series || '/e' || tmdb_id, 3 FROM episode_imdb WHERE imdb = ?1`
	case "tvdb":
		q = `SELECT kind || '/tmdb/' || tmdb, 1 FROM catalog_titles WHERE tvdb = ?1
			UNION ALL SELECT title_key, 2 FROM catalog_items WHERE tvdb = ?1 AND title_key IS NOT NULL`
	default:
		return nil, nil
	}
	var out []string
	seen := map[string]bool{}
	err := s.each(ctx, func(rows *sql.Rows) error {
		var key string
		var rank int
		if err := rows.Scan(&key, &rank); err != nil {
			return err
		}
		if !seen[key] && len(out) < 5 {
			seen[key] = true
			out = append(out, key)
		}
		return nil
	}, `SELECT * FROM (`+q+`) ORDER BY 2`, id)
	return out, err
}

// EpisodeAt returns TMDB's ID of a series' episode at these numbers in
// TMDB's own order (own_episodes, kept with its seasons whatever order
// they're shown in); 0 when none is kept there.
func (s *Store) EpisodeAt(ctx context.Context, series string, season, episode int) (int, error) {
	var id int
	err := s.read.QueryRowContext(ctx, `SELECT tmdb_id FROM own_episodes WHERE series = ? AND season = ? AND episode = ?`,
		series, season, episode).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}
