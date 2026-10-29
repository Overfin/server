package store

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// PickLists are the lists whose titles the libraries lack are Overfin's
// picks, valued like every other title: TMDB's trending and popular titles,
// those like what people here watch ("recommended"), and those in the
// genres they like ("liked").
var PickLists = []string{"trending", "popular", "recommended", "liked"}

// pickLists is PickLists for a query's IN.
var pickLists = "'" + strings.Join(PickLists, "', '") + "'"

// PickSeeds are the films and series people here watched or marked a
// favourite since, the most accounts first, at most limit: those whose
// look-alikes are picks.
func (s *Store) PickSeeds(ctx context.Context, since time.Time, limit int) ([]TitleRef, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT DISTINCT p.account_id, t.title FROM profile_titles t
		JOIN profiles p ON p.id = t.profile_id
		WHERE t.updated_at >= ? AND t.title NOT LIKE 'item/%'
			AND (t.play_count > 0 OR t.played = 1 OR t.position_ms > 0 OR t.favorite = 1)`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := map[string]map[string]bool{} // title key → accounts
	refs := map[string]TitleRef{}
	for rows.Next() {
		var account, key string
		if err := rows.Scan(&account, &key); err != nil {
			return nil, err
		}
		r, ok := ParseTitleKey(key)
		if !ok || r.Source != "tmdb" {
			continue
		}
		if r.Kind != "movie" {
			r = r.Series()
		}
		k := r.Key()
		if accounts[k] == nil {
			accounts[k] = map[string]bool{}
		}
		accounts[k][account] = true
		refs[k] = r
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(refs))
	for k := range refs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if a, b := len(accounts[keys[i]]), len(accounts[keys[j]]); a != b {
			return a > b
		}
		return keys[i] < keys[j]
	})
	out := make([]TitleRef, 0, min(limit, len(keys)))
	for _, k := range keys[:min(limit, len(keys))] {
		out = append(out, refs[k])
	}
	return out, nil
}

// PicksToDescribe are picks whose page wasn't fetched since before, at
// most limit: their page has what the valuation needs (rating,
// popularity, genres, seasons).
func (s *Store) PicksToDescribe(ctx context.Context, before time.Time, limit int) ([]ArtworkTarget, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT DISTINCT t.kind, t.tmdb FROM catalog_lists l
		JOIN catalog_titles t ON t.kind = l.kind AND t.tmdb = l.tmdb
		WHERE l.list IN (`+pickLists+`) AND coalesce(t.details_at, 0) < ?
			AND NOT EXISTS (SELECT 1 FROM catalog_items i WHERE i.kind = t.kind AND i.tmdb = t.tmdb)
		ORDER BY t.kind, t.tmdb LIMIT ?`, before.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArtworkTarget
	for rows.Next() {
		var t ArtworkTarget
		if err := rows.Scan(&t.Kind, &t.TMDB); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// picks adds to f the picks the libraries lack and TMDB described: a film
// as itself, a series as each of its seasons that has aired or airs within
// ReleaseSoon. Seasons TMDB hasn't dated yet are left out (nothing to add).
func (s *Store) picks(ctx context.Context, f *ValuationFacts, now time.Time) error {
	rows, err := s.read.QueryContext(ctx, `SELECT DISTINCT t.kind, t.tmdb, t.details FROM catalog_lists l
		JOIN catalog_titles t ON t.kind = l.kind AND t.tmdb = l.tmdb
		WHERE l.list IN (`+pickLists+`) AND t.details IS NOT NULL
			AND NOT EXISTS (SELECT 1 FROM catalog_items i WHERE i.kind = t.kind AND i.tmdb = t.tmdb)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	soon := now.UTC().Add(ReleaseSoon).Format(time.DateOnly)
	for rows.Next() {
		var kind, tmdb, details string
		if err := rows.Scan(&kind, &tmdb, &details); err != nil {
			return err
		}
		if kind == "movie" {
			f.title(TitleRef{Kind: "movie", Source: "tmdb", ID: tmdb}.Key())
			continue
		}
		var d TitleDetails
		if json.Unmarshal([]byte(details), &d) != nil {
			continue
		}
		if f.pickTVDB == nil {
			f.pickTVDB = map[string]string{}
		}
		f.pickTVDB[tmdb] = d.TVDB
		for _, se := range d.Seasons {
			if se.Number < 1 || se.EpisodeCount < 1 || se.AirDate == "" || se.AirDate > soon {
				continue
			}
			t := f.title(TitleRef{Kind: "season", Source: "tmdb", ID: tmdb, Season: se.Number}.Key())
			if f.seasonAired == nil {
				f.seasonAired = map[string]string{}
			}
			f.seasonAired[t.Key] = se.AirDate
		}
	}
	return rows.Err()
}

// ListPositions are where each title stands in one of discovery's lists,
// from 0, by kind and TMDB ID ("movie/550", "series/1399").
func (s *Store) ListPositions(ctx context.Context, list string) (map[string]int, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT kind, tmdb, position FROM catalog_lists WHERE list = ?`, list)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var kind, tmdb string
		var n int
		if err := rows.Scan(&kind, &tmdb, &n); err != nil {
			return nil, err
		}
		out[kind+"/"+tmdb] = n
	}
	return out, rows.Err()
}

// ReleaseSoon is how close to its release a title the libraries lack may
// be suggested: an app adding it then has it ready for the release.
const ReleaseSoon = 2 * 24 * time.Hour

// HomeRelease is when a film can be watched at home, as far as Overfin
// knows: AtHome (digital, disc or TV), Released (its first release, in
// cinemas), Year; "" or 0 when unknown.
type HomeRelease struct {
	AtHome, Released string
	Year             int
}

// OutBy reports whether the film is out at home by day (YYYY-MM-DD): its
// home release, or 90 days after cinemas when that's all that's known, or,
// without any date, a year before day's.
func (r HomeRelease) OutBy(day time.Time) bool {
	d := day.UTC().Format(time.DateOnly)
	switch {
	case r.AtHome != "":
		return r.AtHome <= d
	case r.Released != "":
		cinemas, err := time.Parse(time.DateOnly, r.Released)
		return err == nil && !cinemas.AddDate(0, 0, 90).After(day.UTC())
	}
	return r.Year > 0 && r.Year < day.UTC().Year()
}

// FilmReleases are the films the catalog knows, by TMDB ID, with what's
// known of their release.
func (s *Store) FilmReleases(ctx context.Context) (map[string]HomeRelease, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT t.tmdb, coalesce(t.year, 0), coalesce(r.released, ''), coalesce(r.at_home, '')
		FROM catalog_titles t LEFT JOIN title_ratings_fetched r ON r.kind = t.kind AND r.tmdb = t.tmdb
		WHERE t.kind = 'movie'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]HomeRelease{}
	for rows.Next() {
		var tmdb string
		var r HomeRelease
		if err := rows.Scan(&tmdb, &r.Year, &r.Released, &r.AtHome); err != nil {
			return nil, err
		}
		out[tmdb] = r
	}
	return out, rows.Err()
}
