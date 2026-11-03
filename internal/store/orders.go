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

// ArrangeSeries numbers a series' seasons and episodes as an order does
// (group ""; TMDB's own), at once: what the files are and what profiles did
// with them. The order says what the files are, so what a profile did with
// a file stays with that file: marks go by the file's numbers while the IDs
// change under them. The series' translations and artwork are fetched again.
func (s *Store) ArrangeSeries(ctx context.Context, series, group, name string, seasons []Season, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := marksToNumbers(ctx, tx, series); err != nil {
		return err
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
	listed := make([]SeasonInfo, 0, len(seasons))
	for _, se := range seasons {
		listed = append(listed, SeasonInfo{Number: se.Number, Name: se.Name, EpisodeCount: len(se.Episodes), AirDate: se.AirDate})
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
	if group == "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM series_orders WHERE series = ?`, series); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, `INSERT INTO series_orders (series, group_id, name, chosen_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (series) DO UPDATE SET group_id = excluded.group_id, name = excluded.name, chosen_at = excluded.chosen_at`,
		series, group, name, at.Unix()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE artwork_fetched SET fetched_at = 0 WHERE key = ?`, "series/tmdb/"+series); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.RefreshTitleKeys(ctx)
}

// SeriesFile is a file of a series, by its numbers, and its name.
type SeriesFile struct {
	Season, Episode int
	Name            string
}

// SeriesFiles lists a series' episode files, for comparing them to orders.
func (s *Store) SeriesFiles(ctx context.Context, itemID string) (tmdb string, files []SeriesFile, err error) {
	var id sql.NullString
	err = s.read.QueryRowContext(ctx, `SELECT tmdb FROM catalog_items WHERE id = ? AND kind = 'series'`, itemID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	rows, err := s.read.QueryContext(ctx, `SELECT season_number, episode_number, name FROM catalog_items
		WHERE kind = 'episode' AND series_id = ? AND season_number IS NOT NULL AND episode_number IS NOT NULL`, itemID)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f SeriesFile
		if err := rows.Scan(&f.Season, &f.Episode, &f.Name); err != nil {
			return "", nil, err
		}
		files = append(files, f)
	}
	return id.String, files, rows.Err()
}
