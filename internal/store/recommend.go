package store

import (
	"context"
	"database/sql"
	"math"
	"strings"
	"sync"
	"time"
)

// Recommendations: what Overfin knows of the library's films and series
// to tell which fit a profile, and what the profile watched. What a
// profile watched is read for the page being shown, never kept (rule 40):
// a profile's recommendations are its own. The library's facts, no one's,
// are kept in memory a few seconds.

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

// factsFor is how long the library's facts are kept in memory: a home
// page asks for two rows of recommendations at once, and every profile's
// are worked out from the same facts. What each profile may see and what
// it watched are read again each time; a change to the library shows
// within this time.
const factsFor = 30 * time.Second

// factsCache is the library's facts, read in one pass: every film's and
// series' item, in the order read.
type factsCache struct {
	mu    sync.Mutex
	at    time.Time
	items []itemFacts
}

type itemFacts struct {
	id    string
	facts TitleFacts
}

// FactsOfItems returns the facts of the films and series behind library
// items (by Jellyfin ID), by title key, each title once. They're the
// caller's own copies, but share their genres: read only.
func (s *Store) FactsOfItems(ctx context.Context, itemIDs []string) (map[string]*TitleFacts, error) {
	items, err := s.libraryFacts(ctx)
	if err != nil {
		return nil, err
	}
	want := make(map[string]bool, len(itemIDs))
	for _, id := range itemIDs {
		want[id] = true
	}
	out := map[string]*TitleFacts{}
	for _, it := range items {
		if !want[it.id] {
			continue
		}
		f := it.facts
		if old, seen := out[f.Key]; !seen || f.Latest.After(old.Latest) {
			out[f.Key] = &f
		}
	}
	return out, nil
}

// libraryFacts returns the facts of every film and series item, read
// again when older than factsFor. Callers asking meanwhile wait for the
// one reading them, and share what it read.
func (s *Store) libraryFacts(ctx context.Context) ([]itemFacts, error) {
	c := &s.facts
	c.mu.Lock()
	defer c.mu.Unlock()
	if now := s.now(); c.items != nil && now.Sub(c.at) < factsFor && !now.Before(c.at) {
		return c.items, nil
	}
	// One pass over every film and series: far quicker than asking for
	// thousands by their IDs. TMDB's rating from its index (see
	// fromTMDB).
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
	items := []itemFacts{}
	for rows.Next() {
		var it itemFacts
		f := &it.facts
		var latest int64
		var genres *string
		if err := rows.Scan(&it.id, &f.Key, &f.Kind, &f.Rating, &latest, &f.Age, &genres); err != nil {
			return nil, err
		}
		f.Latest = time.Unix(latest, 0)
		if genres != nil {
			for _, g := range strings.Split(*genres, ",") {
				if n := atoi(g); n > 0 {
					f.Genres = append(f.Genres, n)
				}
			}
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	c.items, c.at = items, s.now()
	return items, nil
}

// halfLife is how long it takes for what a profile watched to count half
// as much in its tastes.
const halfLife = 180 * 24 * time.Hour

// watchedTitles are the films and series a profile watched (to the end,
// or started; a series by any of its episodes), in SQL, with when: its
// one argument is the profile's ID.
const watchedTitles = `SELECT CASE WHEN title LIKE 'movie/tmdb/%' THEN 'movie' ELSE 'series' END AS kind,
		CASE WHEN title LIKE 'movie/tmdb/%' THEN substr(title, 12)
			ELSE substr(title, 13, CASE WHEN instr(substr(title, 13), '/') > 0
				THEN instr(substr(title, 13), '/') - 1 ELSE length(title) END) END AS tmdb,
		coalesce(last_played_at, updated_at) AS at
	FROM profile_titles
	WHERE profile_id = ? AND (played = 1 OR position_ms > 0)
		AND (title LIKE 'movie/tmdb/%' OR title LIKE 'series/tmdb/%')`

// WatchedGenres weighs the genres of the films and series a profile
// watched (to the end, or started; a series by any of its episodes), the
// recent more than the old, and gives those titles' keys.
func (s *Store) WatchedGenres(ctx context.Context, profileID string, now time.Time) (map[int]float64, map[string]bool, error) {
	// Each title once, with its genres, in one query: the history is read
	// once (a query for each title took a fifth of a second for a profile
	// that watched thousands, and a second query for the genres read the
	// history again). Ordered by title then genre, so the weights add up
	// in the same order every time.
	out, keys := map[int]float64{}, map[string]bool{}
	var last string
	var weight float64
	err := s.each(ctx, func(rows *sql.Rows) error {
		var kind, tmdb string
		var at int64
		var g *int
		if err := rows.Scan(&kind, &tmdb, &at, &g); err != nil {
			return err
		}
		key := kind + "/tmdb/" + tmdb
		if key != last {
			last = key
			keys[key] = true
			age := now.Sub(time.Unix(at, 0))
			weight = math.Pow(0.5, max(age.Hours(), 0)/halfLife.Hours())
		}
		if g != nil {
			out[*g] += weight
		}
		return nil
	}, `SELECT w.kind, w.tmdb, w.at, g.genre
		FROM (SELECT kind, tmdb, max(at) AS at FROM (`+watchedTitles+`) GROUP BY kind, tmdb) w
		LEFT JOIN catalog_title_genres g ON g.kind = w.kind AND g.tmdb = w.tmdb
		ORDER BY w.kind, w.tmdb, g.genre`, profileID)
	if err != nil {
		return nil, nil, err
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
