package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// SeriesOrder is the TMDB order an admin chose for a series, by its group's
// ID and name; "" when it has TMDB's own.
func (s *Store) SeriesOrder(ctx context.Context, series string) (group, name string, err error) {
	err = s.read.QueryRowContext(ctx, `SELECT group_id, name FROM series_orders WHERE series = ?`, series).Scan(&group, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return group, name, err
}

// ArrangeSeries shows a series' seasons and episodes as an order has them
// (group "": TMDB's own). It changes nothing of which episode a file is, nor
// of what profiles did with it: own is TMDB's own order, kept with it. The
// series' translations are fetched again.
func (s *Store) ArrangeSeries(ctx context.Context, series, group, name string, seasons, own []Season, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, se := range own {
		if err := keepOwnSeason(ctx, tx, series, se); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM catalog_seasons WHERE series = ?`, series); err != nil {
		return err
	}
	for _, table := range []string{"catalog_season_texts", "catalog_episode_texts"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE series = ?`, series); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM catalog_title_texts WHERE kind = 'series' AND tmdb = ?`, series); err != nil {
		return err
	}
	// The order first: where files are shown is worked out from it.
	if group == "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM series_orders WHERE series = ?`, series); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, `INSERT INTO series_orders (series, group_id, name, chosen_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (series) DO UPDATE SET group_id = excluded.group_id, name = excluded.name, chosen_at = excluded.chosen_at`,
		series, group, name, at.Unix()); err != nil {
		return err
	}
	listed := make([]SeasonInfo, 0, len(seasons))
	for _, se := range seasons {
		listed = append(listed, SeasonInfo{Number: se.Number, Name: se.Name, EpisodeCount: len(se.Episodes), AirDate: se.AirDate})
		se.Arranged = group != ""
		if err := setSeason(ctx, tx, series, se, at); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(listed)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE catalog_titles SET details = json_set(details, '$.seasons', json(?))
		WHERE kind = 'series' AND tmdb = ? AND json_valid(details)`, string(raw), series); err != nil {
		return err
	}
	if err := showFiles(ctx, tx, series); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE artwork_fetched SET fetched_at = 0 WHERE key = ?`, "series/tmdb/"+series); err != nil {
		return err
	}
	return tx.Commit()
}

// SeriesFile is a file of a series, by its numbers, and the TMDB ID of the
// episode it is (0: TMDB's own order has none at those numbers).
type SeriesFile struct {
	Season, Episode int
	EpisodeID       int
}

// SeriesFiles lists a series' episode files, for comparing orders with them.
func (s *Store) SeriesFiles(ctx context.Context, itemID string) (tmdb string, files []SeriesFile, err error) {
	var id sql.NullString
	err = s.read.QueryRowContext(ctx, `SELECT tmdb FROM catalog_items WHERE id = ? AND kind = 'series'`, itemID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	rows, err := s.read.QueryContext(ctx, `SELECT i.season_number, i.episode_number, coalesce(o.tmdb_id, 0) FROM catalog_items i
		LEFT JOIN own_episodes o ON o.series = ? AND o.season = i.season_number AND o.episode = i.episode_number
		WHERE i.kind = 'episode' AND i.series_id = ? AND i.season_number IS NOT NULL AND i.episode_number IS NOT NULL`, id.String, itemID)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f SeriesFile
		if err := rows.Scan(&f.Season, &f.Episode, &f.EpisodeID); err != nil {
			return "", nil, err
		}
		files = append(files, f)
	}
	return id.String, files, rows.Err()
}
