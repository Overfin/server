package store

import (
	"context"
	"database/sql"
	"errors"
)

// Episodes go by TMDB's own IDs: series/tmdb/1399/e63056. A file is the
// episode its numbers are in TMDB's own order (own_episodes), which is what
// importing it made sure of; an order an admin chooses only changes where an
// episode is shown (catalog_episodes), never which episode a file is. A key
// by numbers remains for what TMDB gives no ID (a series known by another
// source, a file past TMDB's end, a season not fetched yet); it's turned into
// the ID as soon as the ID is known.

type place struct{ season, episode int }

// Places say where episodes are in their series' orders, by the series' TMDB
// ID and TMDB's ID of the episode.
type Places map[string]map[int]place

// Of gives an episode by ID its numbers; false when its series' order doesn't
// have it. Any other title is as it is.
func (p Places) Of(r TitleRef) (TitleRef, bool) {
	if r.Kind != "episode" || r.EpisodeID == 0 {
		return r, true
	}
	at, ok := p[r.ID][r.EpisodeID]
	r.Season, r.Episode = at.season, at.episode
	return r, ok
}

// addPlaces reads the places of r's series, unless they're read already.
func (s *Store) addPlaces(ctx context.Context, p Places, r TitleRef) error {
	if r.Source != "tmdb" || p[r.ID] != nil {
		return nil
	}
	p[r.ID] = map[int]place{}
	return s.readPlaces(ctx, p, `WHERE series = ? AND tmdb_id IS NOT NULL`, r.ID)
}

// OwnPlaces reads where every series' episodes are in TMDB's own order: the
// files' numbers, which apps go by whatever a series is shown in.
func (s *Store) OwnPlaces(ctx context.Context) (Places, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT series, tmdb_id, season, episode FROM own_episodes ORDER BY season = 0, season, episode`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	p := Places{}
	for rows.Next() {
		var series string
		var id int
		var at place
		if err := rows.Scan(&series, &id, &at.season, &at.episode); err != nil {
			return nil, err
		}
		if p[series] == nil {
			p[series] = map[int]place{}
		}
		if _, seen := p[series][id]; !seen {
			p[series][id] = at
		}
	}
	return p, rows.Err()
}

// readPlaces reads places. An order can list an episode twice: it's where
// it's first listed, specials last.
func (s *Store) readPlaces(ctx context.Context, p Places, where string, args ...any) error {
	rows, err := s.read.QueryContext(ctx, `SELECT series, tmdb_id, season, episode FROM catalog_episodes `+where+`
		ORDER BY season = 0, season, episode`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var series string
		var id int
		var at place
		if err := rows.Scan(&series, &id, &at.season, &at.episode); err != nil {
			return err
		}
		if p[series] == nil {
			p[series] = map[int]place{}
		}
		if _, seen := p[series][id]; !seen {
			p[series][id] = at
		}
	}
	return rows.Err()
}

// PlaceTitle completes an episode's ref: the numbers of one by ID, when its
// series' order has it; the ID of one by numbers, when TMDB's is known. Any
// other title is as it is.
func (s *Store) PlaceTitle(ctx context.Context, r TitleRef) (TitleRef, error) {
	if r.Kind != "episode" || r.TMDB() == "" {
		return r, nil
	}
	if r.EpisodeID > 0 {
		err := s.read.QueryRowContext(ctx, `SELECT season, episode FROM catalog_episodes WHERE series = ? AND tmdb_id = ?
			ORDER BY season = 0, season, episode LIMIT 1`, r.ID, r.EpisodeID).Scan(&r.Season, &r.Episode)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return r, err
		}
		return r, nil
	}
	var id sql.NullInt64
	err := s.read.QueryRowContext(ctx, `SELECT tmdb_id FROM catalog_episodes WHERE series = ? AND season = ? AND episode = ?`,
		r.ID, r.Season, r.Episode).Scan(&id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return r, err
	}
	r.EpisodeID = int(id.Int64)
	return r, nil
}

// keepOwnSeason keeps a season as TMDB's own order has it, and gives its files
// and what profiles did with its episodes their keys by ID.
func keepOwnSeason(ctx context.Context, tx *sql.Tx, series string, se Season) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM own_episodes WHERE series = ? AND season = ?`, series, se.Number); err != nil {
		return err
	}
	for _, e := range se.Episodes {
		if e.ID <= 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO own_episodes (series, season, episode, tmdb_id) VALUES (?, ?, ?, ?)`,
			series, se.Number, e.Number, e.ID); err != nil {
			return err
		}
	}
	if err := filesToIDs(ctx, tx, series, se.Number); err != nil {
		return err
	}
	return marksToIDs(ctx, tx, series, se.Number)
}

// filesToIDs renames a series' episode files to the IDs at their numbers in
// TMDB's own order; a season below 0: all its seasons'.
func filesToIDs(ctx context.Context, tx *sql.Tx, series string, season int) error {
	_, err := tx.ExecContext(ctx, `UPDATE catalog_items SET title_key = (SELECT 'series/tmdb/' || o.series || '/e' || o.tmdb_id
			FROM own_episodes o WHERE o.series = ?1 AND o.season = catalog_items.season_number
			AND o.episode = catalog_items.episode_number)
		WHERE kind = 'episode' AND (?2 < 0 OR season_number = ?2)
			AND series_id IN (SELECT id FROM catalog_items WHERE kind = 'series' AND tmdb = ?1)
			AND EXISTS (SELECT 1 FROM own_episodes o WHERE o.series = ?1 AND o.season = catalog_items.season_number
				AND o.episode = catalog_items.episode_number)`, series, season)
	return err
}

// showFiles says where a series' chosen order puts its files: where their
// episodes are in it; a series on TMDB's own, nothing: their numbers.
func showFiles(ctx context.Context, tx *sql.Tx, series string) error {
	_, err := tx.ExecContext(ctx, `UPDATE catalog_items SET
			shown_season = CASE WHEN EXISTS (SELECT 1 FROM series_orders WHERE series = ?1) THEN (SELECT e.season FROM own_episodes o
				JOIN catalog_episodes e ON e.series = o.series AND e.tmdb_id = o.tmdb_id
				WHERE o.series = ?1 AND o.season = catalog_items.season_number AND o.episode = catalog_items.episode_number
				ORDER BY e.season = 0, e.season, e.episode LIMIT 1) END,
			shown_episode = CASE WHEN EXISTS (SELECT 1 FROM series_orders WHERE series = ?1) THEN (SELECT e.episode FROM own_episodes o
				JOIN catalog_episodes e ON e.series = o.series AND e.tmdb_id = o.tmdb_id
				WHERE o.series = ?1 AND o.season = catalog_items.season_number AND o.episode = catalog_items.episode_number
				ORDER BY e.season = 0, e.season, e.episode LIMIT 1) END
		WHERE kind = 'episode' AND series_id IN (SELECT id FROM catalog_items WHERE kind = 'series' AND tmdb = ?1)`, series)
	return err
}

// mergeMarks adds what profiles did under one key to what they did under
// another, the way two copies of one title add up (MergePersonal), then drops
// the first: from selects (profile_id, title, new) from profile_titles p.
func mergeMarks(ctx context.Context, tx *sql.Tx, from string, args ...any) error {
	rows, err := tx.QueryContext(ctx, `SELECT p.profile_id, p.title, n.new `+from, args...)
	if err != nil {
		return err
	}
	type move struct{ profile, title, new string }
	var moves []move
	for rows.Next() {
		var m move
		if err := rows.Scan(&m.profile, &m.title, &m.new); err != nil {
			rows.Close()
			return err
		}
		moves = append(moves, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, m := range moves {
		if _, err := tx.ExecContext(ctx, `INSERT INTO profile_titles (profile_id, title, item_id, position_ms, played, play_count,
				last_played_at, favorite, updated_at)
			SELECT profile_id, ?, item_id, position_ms, played, play_count, last_played_at, favorite, updated_at
			FROM profile_titles WHERE profile_id = ? AND title = ? AND true
			ON CONFLICT (profile_id, title) DO UPDATE SET
				item_id = coalesce(excluded.item_id, item_id),
				position_ms = max(position_ms, excluded.position_ms),
				played = max(played, excluded.played),
				play_count = max(play_count, excluded.play_count),
				last_played_at = max(coalesce(last_played_at, 0), coalesce(excluded.last_played_at, 0)),
				favorite = max(favorite, excluded.favorite),
				updated_at = max(updated_at, excluded.updated_at)`, m.new, m.profile, m.title); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM profile_titles WHERE profile_id = ? AND title = ?`, m.profile, m.title); err != nil {
			return err
		}
	}
	return nil
}

// marksToIDs renames what profiles did under an episode's numbers in TMDB's
// own order to its ID: a series' season, or below 0 all its seasons.
func marksToIDs(ctx context.Context, tx *sql.Tx, series string, season int) error {
	return mergeMarks(ctx, tx, `FROM (SELECT 'series/tmdb/' || series || '/s' || season || '/e' || episode AS old,
			'series/tmdb/' || series || '/e' || tmdb_id AS new
			FROM own_episodes WHERE series = ? AND (? < 0 OR season = ?)) n
		JOIN profile_titles p ON p.title = n.old`, series, season, season)
}

// marksToNumbers renames what profiles did under a series' episodes' IDs to
// their numbers in TMDB's own order, the files': for a series identified as
// another, whose episodes have other IDs. An episode listed twice is where
// it's first listed.
func marksToNumbers(ctx context.Context, tx *sql.Tx, series string) error {
	return mergeMarks(ctx, tx, `FROM (SELECT 'series/tmdb/' || series || '/e' || tmdb_id AS old,
			'series/tmdb/' || series || '/s' || season || '/e' || episode AS new
			FROM (SELECT *, row_number() OVER (PARTITION BY tmdb_id ORDER BY season = 0, season, episode) AS r
				FROM own_episodes WHERE series = ?) WHERE r = 1) n
		JOIN profile_titles p ON p.title = n.old`, series)
}

// ownEpisodeKey is the key by ID of an episode by its numbers in TMDB's own
// order, for what names files by their numbers (an import); the same key
// when TMDB has no such episode.
func (s *Store) ownEpisodeKey(ctx context.Context, r TitleRef) (string, error) {
	if r.TMDB() == "" || r.EpisodeID > 0 {
		return r.Key(), nil
	}
	var id int
	err := s.read.QueryRowContext(ctx, `SELECT tmdb_id FROM own_episodes WHERE series = ? AND season = ? AND episode = ?`,
		r.ID, r.Season, r.Episode).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return r.Key(), nil
	}
	if err != nil {
		return "", err
	}
	r.EpisodeID = id
	return r.Key(), nil
}
