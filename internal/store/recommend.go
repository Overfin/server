package store

import (
	"context"
	"math"
	"strings"
	"time"
)

// Recommendations: what Overfin knows of the library's films and series
// to tell which fit a profile, and what the profile watched. Read for the
// page being shown, never kept (rule 40): a profile's recommendations are
// its own.

// TitleFacts are what a film's or a series' fit is worked out from.
type TitleFacts struct {
	Key    string // movie/tmdb/949, series/tmdb/1399
	Kind   string
	Genres []int // TMDB's
	Rating float64
	// Latest is when it, or for a series its newest episode, was added.
	Latest time.Time
	// Age is its age (TitleAge); nil when unknown.
	Age *int
}

// FactsOfItems returns the facts of the films and series behind library
// items (by Jellyfin ID), by title key, each title once.
func (s *Store) FactsOfItems(ctx context.Context, itemIDs []string) (map[string]*TitleFacts, error) {
	// One pass over every film and series, kept when asked for: far
	// quicker than asking for thousands by their IDs.
	// TMDB's rating from its index (see fromTMDB).
	want := make(map[string]bool, len(itemIDs))
	for _, id := range itemIDs {
		want[id] = true
	}
	rows, err := s.db.QueryContext(ctx, `SELECT i.id, i.title_key, i.kind, coalesce(t.rating, 0),
			max(coalesce(i.added_at, 0), coalesce((SELECT max(e.added_at) FROM catalog_items e WHERE e.series_id = i.id), 0)),
			a.age,
			(SELECT group_concat(g.genre) FROM catalog_title_genres g WHERE g.kind = i.kind AND g.tmdb = i.tmdb)
		FROM catalog_items i
		LEFT JOIN catalog_titles t INDEXED BY catalog_titles_listing ON t.kind = i.kind AND t.tmdb = i.tmdb
		LEFT JOIN title_ages a ON a.kind = i.kind AND a.tmdb = i.tmdb
		WHERE i.kind IN ('movie', 'series') AND i.title_key IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*TitleFacts{}
	for rows.Next() {
		var id string
		var f TitleFacts
		var latest int64
		var age *int
		var genres *string
		if err := rows.Scan(&id, &f.Key, &f.Kind, &f.Rating, &latest, &age, &genres); err != nil {
			return nil, err
		}
		if !want[id] {
			continue
		}
		f.Latest, f.Age = time.Unix(latest, 0), age
		if genres != nil {
			for _, g := range strings.Split(*genres, ",") {
				if n := atoi(g); n > 0 {
					f.Genres = append(f.Genres, n)
				}
			}
		}
		if old, seen := out[f.Key]; !seen || f.Latest.After(old.Latest) {
			out[f.Key] = &f
		}
	}
	return out, rows.Err()
}

// halfLife is how long it takes for what a profile watched to count half
// as much in its tastes.
const halfLife = 180 * 24 * time.Hour

// WatchedGenres weighs the genres of the films and series a profile
// watched (to the end, or started; a series by any of its episodes), the
// recent more than the old, and gives those titles' keys.
func (s *Store) WatchedGenres(ctx context.Context, profileID string, now time.Time) (map[int]float64, map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT w.kind, w.tmdb, max(w.at) FROM (
			SELECT CASE WHEN title LIKE 'movie/tmdb/%' THEN 'movie' ELSE 'series' END AS kind,
				CASE WHEN title LIKE 'movie/tmdb/%' THEN substr(title, 12)
					ELSE substr(title, 13, CASE WHEN instr(substr(title, 13), '/') > 0
						THEN instr(substr(title, 13), '/') - 1 ELSE length(title) END) END AS tmdb,
				coalesce(last_played_at, updated_at) AS at
			FROM profile_titles
			WHERE profile_id = ? AND (played = 1 OR position_ms > 0)
				AND (title LIKE 'movie/tmdb/%' OR title LIKE 'series/tmdb/%')) w
		GROUP BY w.kind, w.tmdb`, profileID)
	if err != nil {
		return nil, nil, err
	}
	type seen struct {
		kind, tmdb string
		weight     float64
	}
	var titles []seen
	for rows.Next() {
		var t seen
		var at int64
		if err := rows.Scan(&t.kind, &t.tmdb, &at); err != nil {
			rows.Close()
			return nil, nil, err
		}
		age := now.Sub(time.Unix(at, 0))
		t.weight = math.Pow(0.5, max(age.Hours(), 0)/halfLife.Hours())
		titles = append(titles, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	out, keys := map[int]float64{}, map[string]bool{}
	for _, t := range titles {
		keys[t.kind+"/tmdb/"+t.tmdb] = true
		genres, err := s.ids(ctx, `SELECT genre FROM catalog_title_genres WHERE kind = ? AND tmdb = ?`, t.kind, t.tmdb)
		if err != nil {
			return nil, nil, err
		}
		for _, g := range genres {
			out[atoi(g)] += t.weight
		}
	}
	return out, keys, nil
}

// SetProfileKidsTitles sets whether a profile wants kids titles in its
// recommendations (nil: not chosen, off), within its account.
func (s *Store) SetProfileKidsTitles(ctx context.Context, accountID, profileID string, on *bool) error {
	var v any
	if on != nil {
		v = boolInt(*on)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE profiles SET kids_titles = ? WHERE id = ? AND account_id = ?`, v, profileID, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
