package store

import (
	"context"
	"database/sql"
	"time"
)

// EpisodeToLink is an episode whose IMDb ID TMDB is to be asked: by TMDB's
// ID of it, and its numbers in TMDB's own order, which TMDB asks by.
type EpisodeToLink struct {
	Series          string // the series' TMDB ID
	TMDBID          int
	Season, Episode int
}

// EpisodesToLink returns up to limit aired episodes (by now) of the
// libraries' series whose IMDb ID TMDB hasn't been asked since before, or,
// for those it gave none, since noneBefore (TMDB often adds it a while
// after an episode airs): never asked first, then the oldest. An episode
// that hasn't aired has no IMDb ID to give yet.
func (s *Store) EpisodesToLink(ctx context.Context, now, before, noneBefore time.Time, limit int) ([]EpisodeToLink, error) {
	var out []EpisodeToLink
	err := s.each(ctx, func(rows *sql.Rows) error {
		var e EpisodeToLink
		if err := rows.Scan(&e.Series, &e.TMDBID, &e.Season, &e.Episode); err != nil {
			return err
		}
		out = append(out, e)
		return nil
	}, `SELECT o.series, o.tmdb_id, o.season, o.episode FROM own_episodes o
		LEFT JOIN episode_imdb i ON i.series = o.series AND i.tmdb_id = o.tmdb_id
		WHERE (i.checked_at IS NULL OR i.checked_at < ? OR (i.imdb IS NULL AND i.checked_at < ?))
			AND EXISTS (SELECT 1 FROM catalog_items c WHERE c.kind = 'series' AND c.tmdb = o.series)
			AND EXISTS (SELECT 1 FROM catalog_episodes e WHERE e.series = o.series AND e.tmdb_id = o.tmdb_id
				AND e.air_date IS NOT NULL AND e.air_date <= ?)
		GROUP BY o.series, o.tmdb_id
		ORDER BY i.checked_at IS NOT NULL, i.checked_at, o.series, o.season, o.episode
		LIMIT ?`, before.Unix(), noneBefore.Unix(), now.UTC().Format(time.DateOnly), limit)
	return out, err
}

// SetEpisodeIMDb keeps the IMDb ID TMDB gives an episode ("" for none).
func (s *Store) SetEpisodeIMDb(ctx context.Context, series string, tmdbID int, imdb string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO episode_imdb (series, tmdb_id, imdb, checked_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (series, tmdb_id) DO UPDATE SET imdb = excluded.imdb, checked_at = excluded.checked_at`,
		series, tmdbID, nullString(imdb), at.Unix())
	return err
}

// PurgeEpisodeIMDb deletes what TMDB said of episodes before before: like
// the rest of TMDB's data, it isn't kept past TMDB's six months.
func (s *Store) PurgeEpisodeIMDb(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM episode_imdb WHERE checked_at < ?`, before.Unix())
	return err
}
