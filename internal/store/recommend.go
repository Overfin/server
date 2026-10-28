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
	out := map[string]*TitleFacts{}
	for start := 0; start < len(itemIDs); start += 500 {
		chunk := itemIDs[start:min(start+500, len(itemIDs))]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := s.db.QueryContext(ctx, `SELECT i.title_key, i.kind, coalesce(t.rating, 0),
				coalesce((SELECT max(e.added_at) FROM catalog_items e WHERE e.id = i.id OR e.series_id = i.id), 0),
				a.age,
				(SELECT group_concat(g.genre) FROM catalog_title_genres g WHERE g.kind = i.kind AND g.tmdb = i.tmdb)
			FROM catalog_items i
			LEFT JOIN catalog_titles t ON t.kind = i.kind AND t.tmdb = i.tmdb
			LEFT JOIN title_ages a ON a.kind = i.kind AND a.tmdb = i.tmdb
			WHERE i.id IN (`+marks(len(chunk))+`) AND i.kind IN ('movie', 'series') AND i.title_key IS NOT NULL`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var f TitleFacts
			var latest int64
			var age *int
			var genres *string
			if err := rows.Scan(&f.Key, &f.Kind, &f.Rating, &latest, &age, &genres); err != nil {
				rows.Close()
				return nil, err
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
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// halfLife is how long it takes for what a profile watched to count half
// as much in its tastes.
const halfLife = 180 * 24 * time.Hour

// WatchedGenres weighs the genres of the films and series a profile
// watched (to the end, or started; a series by any of its episodes), the
// recent more than the old, and says how many titles they came from.
func (s *Store) WatchedGenres(ctx context.Context, profileID string, now time.Time) (map[int]float64, int, error) {
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
		return nil, 0, err
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
			return nil, 0, err
		}
		age := now.Sub(time.Unix(at, 0))
		t.weight = math.Pow(0.5, max(age.Hours(), 0)/halfLife.Hours())
		titles = append(titles, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	out := map[int]float64{}
	for _, t := range titles {
		genres, err := s.ids(ctx, `SELECT genre FROM catalog_title_genres WHERE kind = ? AND tmdb = ?`, t.kind, t.tmdb)
		if err != nil {
			return nil, 0, err
		}
		for _, g := range genres {
			out[atoi(g)] += t.weight
		}
	}
	return out, len(titles), nil
}

// AccountHasKids reports whether an account has a kids profile.
func (s *Store) AccountHasKids(ctx context.Context, accountID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM profiles WHERE account_id = ? AND kind = 'kids'`, accountID).Scan(&n)
	return n > 0, err
}

// SetProfileKidsTitles sets whether a profile wants kids titles in its
// recommendations (nil: as its account does), within its account.
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
