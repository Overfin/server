package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
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
	// Titles are the films and seasons the libraries have, those anyone
	// watched, marked or asked for, and Overfin's picks, by key.
	Titles map[string]*ValuedTitle
	// Tastes are what accounts said they like, by account ID, when the
	// operator has people asked; empty otherwise.
	Tastes map[string]Tastes

	// seasonAired are the picks' seasons' first air dates, from their
	// series' page, by key: for seasons not fetched on their own.
	seasonAired map[string]string
	// pickTVDB are the picked series' TheTVDB IDs, from their page, by
	// TMDB ID: the libraries don't have them to say.
	pickTVDB map[string]string
}

// ValuationProfile is who a profile is, for the valuation.
type ValuationProfile struct {
	AccountID string
	Kids      bool
	// ChildrensContent: another profile that set "Contenu pour enfants".
	ChildrensContent bool
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
	Key  string
	Name string // the film's or the series' name
	// IMDb and TVDB are the other IDs the libraries know the film or the
	// series by; "" when none has it.
	IMDb, TVDB string
	Season     int // seasons only
	InLibrary  bool
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
	// Genres are TMDB's genre IDs, the series' for a season.
	Genres []int
}

// activeFor is how long an account counts after it was last used.
const activeFor = 183 * 24 * time.Hour

// ValuationFacts reads what the title valuation needs, as of now, with the
// interests the features keep (by title key).
func (s *Store) ValuationFacts(ctx context.Context, now time.Time, interests []ProfileTitle) (*ValuationFacts, error) {
	f := &ValuationFacts{Profiles: map[string]ValuationProfile{}, Titles: map[string]*ValuedTitle{}, Interests: interests}
	cutoff := now.Add(-activeFor).Unix()
	rows, err := s.read.QueryContext(ctx, `SELECT p.id, p.account_id, p.kind = 'kids', coalesce(p.kids_titles, 0) = 1,
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
		if err := rows.Scan(&id, &p.AccountID, &p.Kids, &p.ChildrensContent, &p.Active); err != nil {
			rows.Close()
			return nil, err
		}
		f.Profiles[id] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.read.QueryContext(ctx, `SELECT profile_id, title, play_count, played, position_ms, favorite,
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

	rows, err = s.read.QueryContext(ctx, `SELECT account_id, title, coalesce(seasons, '') FROM requests
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
	rows, err = s.read.QueryContext(ctx, `SELECT title_key, max(coalesce(runtime_ms, 0)), coalesce(min(added_at), 0)
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
	// And Overfin's picks, which the libraries lack.
	if err := s.picks(ctx, f, now); err != nil {
		return nil, err
	}
	if err := s.describeValued(ctx, f, now); err != nil {
		return nil, err
	}
	asked, err := s.TastesAsked(ctx)
	if err != nil {
		return nil, err
	}
	f.Tastes = map[string]Tastes{}
	if asked {
		if f.Tastes, err = s.AllTastes(ctx); err != nil {
			return nil, err
		}
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
// ratings, popularity, dates, and each season's aired episodes. Each
// table is read once, whole: a few queries for each of thousands of
// titles took seconds on a large library.
func (s *Store) describeValued(ctx context.Context, f *ValuationFacts, now time.Time) error {
	// The other IDs the libraries know each film and series by, by kind
	// and key.
	type otherIDs struct{ imdb, tvdb string }
	others := map[string]otherIDs{}
	err := s.each(ctx, func(rows *sql.Rows) error {
		var kind, key string
		var imdb, tvdb sql.NullString
		if err := rows.Scan(&kind, &key, &imdb, &tvdb); err != nil {
			return err
		}
		others[kind+" "+key] = otherIDs{imdb.String, tvdb.String}
		return nil
	}, `SELECT kind, title_key, max(imdb), max(tvdb) FROM catalog_items
		WHERE kind IN ('movie', 'series') AND title_key IS NOT NULL GROUP BY kind, title_key`)
	if err != nil {
		return err
	}
	// TMDB's genres of each film and series, by kind and TMDB ID.
	genres := map[string][]int{}
	err = s.each(ctx, func(rows *sql.Rows) error {
		var kind, tmdb string
		var g int
		if err := rows.Scan(&kind, &tmdb, &g); err != nil {
			return err
		}
		genres[kind+"/"+tmdb] = append(genres[kind+"/"+tmdb], g)
		return nil
	}, `SELECT kind, tmdb, genre FROM catalog_title_genres ORDER BY kind, tmdb, genre`)
	if err != nil {
		return err
	}
	// TMDB's page of each film and series, by kind and TMDB ID.
	type top struct {
		name               string
		rating, popularity float64
		votes              int
		released           string
	}
	tops := map[string]top{}
	err = s.each(ctx, func(rows *sql.Rows) error {
		var kind, tmdb string
		var t top
		if err := rows.Scan(&kind, &tmdb, &t.name, &t.rating, &t.votes, &t.popularity, &t.released); err != nil {
			return err
		}
		tops[kind+"/"+tmdb] = t
		return nil
	}, `SELECT kind, tmdb, name, coalesce(rating, 0), coalesce(rating_votes, 0), coalesce(popularity, 0),
		coalesce(release_date, '') FROM catalog_titles`)
	if err != nil {
		return err
	}
	read := func(kind, tmdb string) (top, error) {
		k := kind + "/" + tmdb
		if t, ok := tops[k]; ok {
			return t, nil
		}
		// A library item TMDB's data hasn't come for yet.
		var t top
		err := s.read.QueryRowContext(ctx, `SELECT name FROM catalog_items WHERE kind = ? AND tmdb = ? LIMIT 1`,
			kind, tmdb).Scan(&t.name)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return t, err
		}
		tops[k] = t
		return t, nil
	}
	// Each season's first air date, and its episodes aired by today, by
	// series (TMDB ID) and season number.
	aired := map[string]string{}
	err = s.each(ctx, func(rows *sql.Rows) error {
		var series, air string
		var season int
		if err := rows.Scan(&series, &season, &air); err != nil {
			return err
		}
		aired[series+"/"+strconv.Itoa(season)] = air
		return nil
	}, `SELECT series, season, coalesce(air_date, '') FROM catalog_seasons`)
	if err != nil {
		return err
	}
	type episode struct {
		number  int
		minutes int64
	}
	episodes := map[string][]episode{}
	today := now.UTC().Format(time.DateOnly)
	err = s.each(ctx, func(rows *sql.Rows) error {
		var series string
		var season int
		var e episode
		if err := rows.Scan(&series, &season, &e.number, &e.minutes); err != nil {
			return err
		}
		k := series + "/" + strconv.Itoa(season)
		episodes[k] = append(episodes[k], e)
		return nil
	}, `SELECT series, season, episode, coalesce(runtime, 0) FROM catalog_episodes
		WHERE air_date IS NOT NULL AND air_date <= ?`, today)
	if err != nil {
		return err
	}

	for _, t := range f.Titles {
		r, _ := ParseTitleKey(t.Key)
		if r.Source != "tmdb" {
			continue
		}
		top := r // the film, or the season's series
		if r.Kind == "season" {
			top = r.Series()
		}
		o := others[top.Kind+" "+top.Key()]
		t.IMDb, t.TVDB = o.imdb, o.tvdb
		if t.TVDB == "" && top.Kind == "series" {
			t.TVDB = f.pickTVDB[r.ID]
		}
		t.Genres = slices.Clone(genres[top.Kind+"/"+r.ID])
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
		season := r.ID + "/" + strconv.Itoa(r.Season)
		air := aired[season]
		if air == "" {
			air = f.seasonAired[t.Key]
		}
		t.Released, _ = time.Parse(time.DateOnly, air)
		for _, e := range episodes[season] {
			if e.minutes > 0 || t.Episodes[e.number] == 0 {
				t.Episodes[e.number] = e.minutes * 60_000
			}
		}
	}
	return nil
}

// each runs a query and hands each of its rows to fn.
func (s *Store) each(ctx context.Context, fn func(*sql.Rows) error, query string, args ...any) error {
	rows, err := s.read.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ValuationWeights are the operator's weights for the title valuation, by
// criterion; nil when never set.
func (s *Store) ValuationWeights(ctx context.Context) (map[string]int, error) {
	var raw []byte
	err := s.read.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'valuation'`).Scan(&raw)
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

// DefaultDiscoveryShare is the discovery share until the operator sets one.
const DefaultDiscoveryShare = 50

// DiscoveryShare is the part, in percent, of the suggestions taken from
// TMDB's trending and popular titles whatever their value.
func (s *Store) DiscoveryShare(ctx context.Context) (int, error) {
	var v []byte
	err := s.read.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'discovery-share'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultDiscoveryShare, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(string(v))
	if err != nil || n < 0 || n > 100 {
		return DefaultDiscoveryShare, nil
	}
	return n, nil
}

// SetDiscoveryShare keeps the discovery share, 0 to 100.
func (s *Store) SetDiscoveryShare(ctx context.Context, share int) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('discovery-share', ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, []byte(strconv.Itoa(share)))
	return err
}
