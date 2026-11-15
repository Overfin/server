package store

import (
	"context"
	"database/sql"
	"strconv"
	"time"
)

// Measures say whether the library follows the people using it, over the
// last Days days, as totals for the whole server: nothing per person and
// nothing per title. Films and seasons count each as one.
type Measures struct {
	Days int `json:"days"`
	// Arrived: films and seasons that arrived; Started, those someone
	// started since; Finished, those someone watched to the end (a season:
	// one profile, every episode the library has of it).
	Arrived  int `json:"arrived"`
	Started  int `json:"started"`
	Finished int `json:"finished"`
	// Left: films and seasons that left; Missed, those someone wished for
	// after they left (still wished, or back since).
	Left   int `json:"left"`
	Missed int `json:"missed"`
	// Library: the films and seasons the libraries have now; Watched,
	// those someone watched some of in those days.
	Library int `json:"library"`
	Watched int `json:"watched"`
}

// measureViewing is one profile's viewing of a film or a season: its
// latest, and its episodes watched to the end with when.
type measureViewing struct {
	last     int64
	played   int64 // a film: when it was last watched to the end
	episodes map[string]int64
}

// LibraryMeasures computes the Measures of the days before now.
func (s *Store) LibraryMeasures(ctx context.Context, now time.Time, days int) (Measures, error) {
	m := Measures{Days: days}
	since := now.AddDate(0, 0, -days).Unix()

	// Each episode the libraries have, by its title key: its season's key
	// (series/tmdb/N/sK, the season's number as Jellyfin has it, like the
	// history's) and how many episodes that season has.
	episodeSeason := map[string]string{}
	seasonEpisodes := map[string]int{}
	err := s.each(ctx, func(rows *sql.Rows) error {
		var key, tmdb string
		var season int
		if err := rows.Scan(&key, &tmdb, &season); err != nil {
			return err
		}
		sk := seasonKey(tmdb, season)
		if _, seen := episodeSeason[key]; !seen {
			seasonEpisodes[sk]++
		}
		episodeSeason[key] = sk
		return nil
	}, `SELECT e.title_key, ser.tmdb, e.season_number FROM catalog_items e JOIN catalog_items ser ON ser.id = e.series_id
		WHERE e.kind = 'episode' AND e.title_key IS NOT NULL AND e.title_key NOT LIKE 'item/%'
			AND ser.tmdb IS NOT NULL AND e.season_number IS NOT NULL`)
	if err != nil {
		return m, err
	}

	// Who watched what, by film or season and profile.
	viewing := map[string]map[string]*measureViewing{}
	err = s.each(ctx, func(rows *sql.Rows) error {
		var profile, key string
		var played bool
		var at int64
		if err := rows.Scan(&profile, &key, &played, &at); err != nil {
			return err
		}
		title, episode := key, ""
		r, ok := ParseTitleKey(key)
		switch {
		case !ok:
			return nil
		case r.Kind == "movie" && r.Source == "tmdb":
		case r.Kind == "episode":
			episode = key
			if sk, ok := episodeSeason[key]; ok {
				title = sk
			} else if r.Source == "tmdb" && r.Season > 0 {
				title = seasonKey(r.ID, r.Season) // a season the libraries no longer have
			} else {
				return nil
			}
		default:
			return nil
		}
		byProfile := viewing[title]
		if byProfile == nil {
			byProfile = map[string]*measureViewing{}
			viewing[title] = byProfile
		}
		v := byProfile[profile]
		if v == nil {
			v = &measureViewing{episodes: map[string]int64{}}
			byProfile[profile] = v
		}
		v.last = max(v.last, at)
		if played && episode != "" {
			v.episodes[episode] = at
		} else if played {
			v.played = max(v.played, at)
		}
		return nil
	}, `SELECT profile_id, title, played = 1 OR play_count > 0, coalesce(last_played_at, updated_at)
		FROM profile_titles WHERE title NOT LIKE 'item/%' AND (play_count > 0 OR played = 1 OR position_ms > 0)`)
	if err != nil {
		return m, err
	}
	// startedSince: someone watched some of it at or after since.
	startedSince := func(title string, at int64) bool {
		for _, v := range viewing[title] {
			if v.last >= at {
				return true
			}
		}
		return false
	}
	// finishedSince: someone watched it to the end at or after since; a
	// season, one profile every episode the libraries have of it.
	finishedSince := func(title string, at int64) bool {
		for _, v := range viewing[title] {
			if v.played >= at && v.played > 0 {
				return true
			}
			if n := seasonEpisodes[title]; n > 0 {
				done := 0
				for _, when := range v.episodes {
					if when >= at {
						done++
					}
				}
				if done >= n {
					return true
				}
			}
		}
		return false
	}

	// What arrived and left in those days, each film or season once: its
	// first arrival, its last departure.
	arrived := map[string]int64{}
	left := map[string]int64{}
	err = s.each(ctx, func(rows *sql.Rows) error {
		var event, kind, item string
		var tmdb sql.NullString
		var season sql.NullInt64
		var at int64
		if err := rows.Scan(&event, &kind, &item, &tmdb, &season, &at); err != nil {
			return err
		}
		title := "item/" + item
		switch {
		case kind == "movie" && tmdb.Valid:
			title = "movie/tmdb/" + tmdb.String
		case kind == "season" && tmdb.Valid && season.Valid:
			title = seasonKey(tmdb.String, int(season.Int64))
		}
		if event == "arrived" {
			if first, ok := arrived[title]; !ok || at < first {
				arrived[title] = at
			}
		} else {
			left[title] = max(left[title], at)
		}
		return nil
	}, `SELECT h.event, h.kind, h.item_id, coalesce(h.tmdb, s.tmdb), h.season, h.at
		FROM title_history h LEFT JOIN catalog_items s ON s.id = h.series_id WHERE h.at >= ?`, since)
	if err != nil {
		return m, err
	}
	for title, at := range arrived {
		m.Arrived++
		if startedSince(title, at) {
			m.Started++
		}
		if finishedSince(title, at) {
			m.Finished++
		}
	}

	// Wished for after it left: a film, a season, or its whole series;
	// still wished, or back since a wish brought it.
	wishedAt := map[string][]int64{}
	err = s.each(ctx, func(rows *sql.Rows) error {
		var title string
		var at int64
		if err := rows.Scan(&title, &at); err != nil {
			return err
		}
		wishedAt[title] = append(wishedAt[title], at)
		return nil
	}, `SELECT title, added_at FROM wishes UNION ALL SELECT title, arrived_at FROM wish_arrivals`)
	if err != nil {
		return m, err
	}
	wishedSince := func(key string, at int64) bool {
		for _, when := range wishedAt[key] {
			if when > at {
				return true
			}
		}
		return false
	}
	for title, at := range left {
		m.Left++
		r, ok := ParseTitleKey(title)
		missed := wishedSince(title, at)
		if ok && r.Kind == "season" {
			missed = missed || wishedSince("series/tmdb/"+r.ID, at)
		}
		if missed {
			m.Missed++
		}
	}

	// The libraries now: each film, and each season with episodes.
	err = s.each(ctx, func(rows *sql.Rows) error {
		var key string
		if err := rows.Scan(&key); err != nil {
			return err
		}
		m.Library++
		if startedSince(key, since) {
			m.Watched++
		}
		return nil
	}, `SELECT DISTINCT title_key FROM catalog_items WHERE kind = 'movie' AND title_key LIKE 'movie/tmdb/%'`)
	if err != nil {
		return m, err
	}
	for season := range seasonEpisodes {
		m.Library++
		if startedSince(season, since) {
			m.Watched++
		}
	}
	return m, nil
}

// seasonKey is a season's title key.
func seasonKey(tmdb string, season int) string {
	return "series/tmdb/" + tmdb + "/s" + strconv.Itoa(season)
}
