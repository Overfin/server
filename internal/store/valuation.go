package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

// ValuationFacts is what the title valuation is computed from: what each
// profile did, who its account is, and what TMDB and the libraries say
// about each film and season. Nothing in it leaves Overfin as it is.
type ValuationFacts struct {
	// Profiles are every profile, by ID.
	Profiles map[string]ValuationProfile
	// Viewings are what profiles watched: films and episodes.
	Viewings []Viewing
	// Favorites are the titles profiles marked: films and series.
	Favorites []ProfileTitle
	// Requests are what accounts asked for: films, seasons, and series
	// (every season).
	Requests []AccountTitle
	// Interests are what profiles keep for later (the watchlist): films,
	// series, seasons, episodes.
	Interests []ProfileTitle
	// Titles are the films and seasons the libraries have, and those
	// anyone watched, marked or asked for, by key.
	Titles map[string]*ValuedTitle
}

// ValuationProfile is who a profile is, for the valuation.
type ValuationProfile struct {
	AccountID string
	Kids      bool
	// Active is whether its account counts: enabled, not expired, and used
	// in the last six months (or made since).
	Active bool
}

// Viewing is how far a profile got with a film or an episode.
type Viewing struct {
	ProfileID  string
	Title      string // a film's or an episode's key
	PlayCount  int
	Played     bool
	PositionMs int64
	LastPlayed time.Time // zero when it was never played here
	Updated    time.Time
}

// ProfileTitle is a title of a profile's.
type ProfileTitle struct {
	ProfileID string
	Title     string
}

// AccountTitle is a title of an account's.
type AccountTitle struct {
	AccountID string
	Title     string
}

// ValuedTitle is a film or a season, as the valuation sees it.
type ValuedTitle struct {
	Key       string
	Name      string // the film's or the series' name
	Season    int    // seasons only
	InLibrary bool
	// Rating, Votes and Popularity are TMDB's, the series' for a season.
	Rating     float64
	Votes      int
	Popularity float64
	// Released is the film's release, or the season's first air date;
	// zero when unknown.
	Released time.Time
	// Added is when it arrived in the libraries: a film's first copy, a
	// season's latest episode; zero when it isn't there.
	Added time.Time
	// RuntimeMs is a film's length; 0 when unknown.
	RuntimeMs int64
	// Episodes are a season's aired episodes and those the libraries
	// have, with their length (0 when unknown).
	Episodes map[int]int64
}

// activeFor is how long an account counts after it was last used.
const activeFor = 183 * 24 * time.Hour

// ValuationFacts reads what the title valuation needs, as of now, with the
// interests the features keep (by title key).
func (s *Store) ValuationFacts(ctx context.Context, now time.Time, interests []ProfileTitle) (*ValuationFacts, error) {
	f := &ValuationFacts{Profiles: map[string]ValuationProfile{}, Titles: map[string]*ValuedTitle{}, Interests: interests}
	cutoff := now.Add(-activeFor).Unix()
	rows, err := s.db.QueryContext(ctx, `SELECT p.id, p.account_id, p.kind = 'kids',
			a.disabled_at IS NULL AND (a.expires_at IS NULL OR a.expires_at > ?) AND (a.created_at >= ?
				OR EXISTS (SELECT 1 FROM sessions x WHERE x.account_id = a.id AND x.last_active_at >= ?)
				OR EXISTS (SELECT 1 FROM profile_titles t JOIN profiles q ON q.id = t.profile_id
					WHERE q.account_id = a.id AND t.updated_at >= ?))
		FROM profiles p JOIN accounts a ON a.id = p.account_id`, now.Unix(), cutoff, cutoff, cutoff)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var p ValuationProfile
		if err := rows.Scan(&id, &p.AccountID, &p.Kids, &p.Active); err != nil {
			rows.Close()
			return nil, err
		}
		f.Profiles[id] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.db.QueryContext(ctx, `SELECT profile_id, title, play_count, played, position_ms, favorite,
			coalesce(last_played_at, 0), updated_at
		FROM profile_titles WHERE title NOT LIKE 'item/%' AND (play_count > 0 OR played = 1 OR position_ms > 0 OR favorite = 1)`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v Viewing
		var favorite bool
		var last, updated int64
		if err := rows.Scan(&v.ProfileID, &v.Title, &v.PlayCount, &v.Played, &v.PositionMs, &favorite, &last, &updated); err != nil {
			rows.Close()
			return nil, err
		}
		r, ok := ParseTitleKey(v.Title)
		if !ok {
			continue
		}
		if favorite && (r.Kind == "movie" || r.Kind == "series") {
			f.Favorites = append(f.Favorites, ProfileTitle{v.ProfileID, v.Title})
		}
		if (r.Kind == "movie" || r.Kind == "episode") && (v.PlayCount > 0 || v.Played || v.PositionMs > 0) {
			if last > 0 {
				v.LastPlayed = time.Unix(last, 0)
			}
			v.Updated = time.Unix(updated, 0)
			f.Viewings = append(f.Viewings, v)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.db.QueryContext(ctx, `SELECT account_id, title, coalesce(seasons, '') FROM requests
		WHERE status NOT IN ('declined', 'failed')`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var account, title, seasons string
		if err := rows.Scan(&account, &title, &seasons); err != nil {
			rows.Close()
			return nil, err
		}
		var list []int
		if seasons != "" {
			_ = json.Unmarshal([]byte(seasons), &list)
		}
		r, ok := ParseTitleKey(title)
		switch {
		case !ok:
		case r.Kind == "movie":
			f.Requests = append(f.Requests, AccountTitle{account, title})
		case r.Kind == "series" && len(list) == 0:
			// Every season: those the valuation knows of, below.
			f.Requests = append(f.Requests, AccountTitle{account, title})
		case r.Kind == "series":
			for _, n := range list {
				f.Requests = append(f.Requests, AccountTitle{account, TitleRef{Kind: "season", Source: r.Source, ID: r.ID, Season: n}.Key()})
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// The films and seasons the libraries have.
	rows, err = s.db.QueryContext(ctx, `SELECT title_key, max(coalesce(runtime_ms, 0)), coalesce(min(added_at), 0)
		FROM catalog_items
		WHERE kind IN ('movie', 'episode') AND title_key IS NOT NULL AND title_key NOT LIKE 'item/%' GROUP BY title_key`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var key string
		var runtime, added int64
		if err := rows.Scan(&key, &runtime, &added); err != nil {
			rows.Close()
			return nil, err
		}
		if t := f.title(key); t != nil {
			t.InLibrary = true
			if r, _ := ParseTitleKey(key); r.Kind == "episode" {
				t.Episodes[r.Episode] = runtime
			} else {
				t.RuntimeMs = runtime
			}
			// A film arrived with its first copy; a season, with its
			// latest episode.
			if at := time.Unix(added, 0); added > 0 && at.After(t.Added) {
				t.Added = at
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// And those people did something with.
	for _, v := range f.Viewings {
		if t := f.title(v.Title); t != nil {
			if r, _ := ParseTitleKey(v.Title); r.Kind == "episode" {
				if _, ok := t.Episodes[r.Episode]; !ok {
					t.Episodes[r.Episode] = 0
				}
			}
		}
	}
	for _, p := range f.Favorites {
		if r, _ := ParseTitleKey(p.Title); r.Kind == "movie" {
			f.title(p.Title)
		}
	}
	for _, a := range f.Requests {
		f.title(a.Title)
	}
	for _, p := range f.Interests {
		f.title(p.Title)
	}
	if err := s.describeValued(ctx, f, now); err != nil {
		return nil, err
	}
	return f, nil
}

// title is the valued film or season key belongs to (an episode's season),
// added if it's new; nil for other keys.
func (f *ValuationFacts) title(key string) *ValuedTitle {
	r, ok := ParseTitleKey(key)
	if !ok {
		return nil
	}
	switch r.Kind {
	case "movie":
	case "season", "episode":
		r = r.SeasonOf()
	default:
		return nil
	}
	k := r.Key()
	t := f.Titles[k]
	if t == nil {
		t = &ValuedTitle{Key: k, Season: r.Season}
		if r.Kind == "season" {
			t.Episodes = map[int]int64{}
		}
		f.Titles[k] = t
	}
	return t
}

// describeValued fills in what TMDB says about the valued titles: names,
// ratings, popularity, dates, and each season's aired episodes.
func (s *Store) describeValued(ctx context.Context, f *ValuationFacts, now time.Time) error {
	type top struct {
		name               string
		rating, popularity float64
		votes              int
		released           string
	}
	tops := map[string]top{}
	read := func(kind, tmdb string) (top, error) {
		k := kind + "/" + tmdb
		if t, ok := tops[k]; ok {
			return t, nil
		}
		var t top
		err := s.db.QueryRowContext(ctx, `SELECT name, coalesce(rating, 0), coalesce(rating_votes, 0),
				coalesce(popularity, 0), coalesce(release_date, '')
			FROM catalog_titles WHERE kind = ? AND tmdb = ?`, kind, tmdb).
			Scan(&t.name, &t.rating, &t.votes, &t.popularity, &t.released)
		if errors.Is(err, sql.ErrNoRows) {
			// A library item TMDB's data hasn't come for yet.
			err = s.db.QueryRowContext(ctx, `SELECT name FROM catalog_items WHERE kind = ? AND tmdb = ? LIMIT 1`,
				kind, tmdb).Scan(&t.name)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return t, err
		}
		tops[k] = t
		return t, nil
	}
	today := now.UTC().Format(time.DateOnly)
	for _, t := range f.Titles {
		r, _ := ParseTitleKey(t.Key)
		if r.Source != "tmdb" {
			continue
		}
		if r.Kind == "movie" {
			d, err := read("movie", r.ID)
			if err != nil {
				return err
			}
			t.Name, t.Rating, t.Votes, t.Popularity = d.name, d.rating, d.votes, d.popularity
			t.Released, _ = time.Parse(time.DateOnly, d.released)
			continue
		}
		d, err := read("series", r.ID)
		if err != nil {
			return err
		}
		t.Name, t.Rating, t.Votes, t.Popularity = d.name, d.rating, d.votes, d.popularity
		var air string
		err = s.db.QueryRowContext(ctx, `SELECT coalesce(air_date, '') FROM catalog_seasons WHERE series = ? AND season = ?`,
			r.ID, r.Season).Scan(&air)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		t.Released, _ = time.Parse(time.DateOnly, air)
		rows, err := s.db.QueryContext(ctx, `SELECT episode, coalesce(runtime, 0) FROM catalog_episodes
			WHERE series = ? AND season = ? AND air_date IS NOT NULL AND air_date <= ?`, r.ID, r.Season, today)
		if err != nil {
			return err
		}
		for rows.Next() {
			var e int
			var minutes int64
			if err := rows.Scan(&e, &minutes); err != nil {
				rows.Close()
				return err
			}
			if minutes > 0 || t.Episodes[e] == 0 {
				t.Episodes[e] = minutes * 60_000
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	return nil
}

// ValuationWeights are the operator's weights for the title valuation, by
// criterion; nil when never set.
func (s *Store) ValuationWeights(ctx context.Context) (map[string]int, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'valuation'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var w map[string]int
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, errors.New("store: the valuation's weights are unreadable: " + strconv.Quote(string(raw)))
	}
	return w, nil
}

// SetValuationWeights keeps the operator's weights; nil goes back to the
// defaults.
func (s *Store) SetValuationWeights(ctx context.Context, w map[string]int) error {
	if w == nil {
		_, err := s.db.ExecContext(ctx, `DELETE FROM meta WHERE name = 'valuation'`)
		return err
	}
	raw, err := json.Marshal(w)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('valuation', ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, raw)
	return err
}
