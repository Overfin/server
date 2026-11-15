package store

import (
	"context"
	"database/sql"
	"time"
)

const (
	// DepartureNotice is how long a title someone wants is held before it
	// may leave: time for a last watch.
	DepartureNotice = 7 * 24 * time.Hour
	// departureStale: a departure the app hasn't asked about for this long
	// is no longer planned, and dropped.
	departureStale = time.Hour
)

// Wanted is who wants which films and seasons, for departures: by title
// key (a film's, a season's or a whole series'), the profiles that keep it
// for later or are partway through it.
type Wanted map[string]map[string]bool

// WantedTitles reads who wants what: the watchlists given (interests, by
// title key: a film, a series, a season or an episode) and what profiles
// are partway through: a film started and not finished, a season with an
// episode watched or started and one the libraries have not finished.
func (s *Store) WantedTitles(ctx context.Context, interests []ProfileTitle) (Wanted, error) {
	return s.wantedTitles(ctx, interests, "")
}

// WantedBy is WantedTitles for one profile alone.
func (s *Store) WantedBy(ctx context.Context, interests []ProfileTitle, profileID string) (Wanted, error) {
	var own []ProfileTitle
	for _, i := range interests {
		if i.ProfileID == profileID {
			own = append(own, i)
		}
	}
	return s.wantedTitles(ctx, own, profileID)
}

func (s *Store) wantedTitles(ctx context.Context, interests []ProfileTitle, profileID string) (Wanted, error) {
	w := Wanted{}
	add := func(key, profile string) {
		if w[key] == nil {
			w[key] = map[string]bool{}
		}
		w[key][profile] = true
	}
	// An episode's season, by the libraries (its season's number as
	// Jellyfin has it) or by its own key; and each season's episodes.
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
		return nil, err
	}
	seasonOf := func(key string) (string, bool) {
		if sk, ok := episodeSeason[key]; ok {
			return sk, true
		}
		if r, ok := ParseTitleKey(key); ok && r.Kind == "episode" && r.Source == "tmdb" && r.Season > 0 {
			return seasonKey(r.ID, r.Season), true
		}
		return "", false
	}
	for _, i := range interests {
		r, ok := ParseTitleKey(i.Title)
		if !ok || r.Source != "tmdb" {
			continue
		}
		switch r.Kind {
		case "movie", "series", "season":
			add(i.Title, i.ProfileID)
		case "episode":
			if sk, ok := seasonOf(i.Title); ok {
				add(sk, i.ProfileID)
			}
		}
	}
	// Partway: by profile and season, the episodes it finished.
	finished := map[string]map[string]int{}
	err = s.each(ctx, func(rows *sql.Rows) error {
		var profile, key string
		var played bool
		if err := rows.Scan(&profile, &key, &played); err != nil {
			return err
		}
		r, ok := ParseTitleKey(key)
		if !ok {
			return nil
		}
		if r.Kind == "movie" && r.Source == "tmdb" {
			if !played {
				add(key, profile)
			}
			return nil
		}
		sk, ok := seasonOf(key)
		if !ok {
			return nil
		}
		if finished[sk] == nil {
			finished[sk] = map[string]int{}
		}
		if played {
			finished[sk][profile]++
		} else {
			finished[sk][profile] += 0
		}
		return nil
	}, `SELECT profile_id, title, played = 1 OR play_count > 0 FROM profile_titles
		WHERE title NOT LIKE 'item/%' AND (play_count > 0 OR played = 1 OR position_ms > 0) AND (? = '' OR profile_id = ?)`,
		profileID, profileID)
	if err != nil {
		return nil, err
	}
	for sk, byProfile := range finished {
		for profile, done := range byProfile {
			if n := seasonEpisodes[sk]; n == 0 || done < n {
				add(sk, profile)
			}
		}
	}
	return w, nil
}

// Wants reports whether profile wants the title (a film, a season or a
// whole series: one of its seasons, or the series itself).
func (w Wanted) Wants(key, profile string) bool {
	if w[key][profile] {
		return true
	}
	r, ok := ParseTitleKey(key)
	if !ok {
		return false
	}
	switch r.Kind {
	case "season":
		return w["series/tmdb/"+r.ID][profile]
	case "series":
		prefix := key + "/s"
		for k, profiles := range w {
			if profiles[profile] && len(k) > len(prefix) && k[:len(prefix)] == prefix {
				return true
			}
		}
	}
	return false
}

// Anyone reports whether someone wants the title.
func (w Wanted) Anyone(key string) bool {
	if len(w[key]) > 0 {
		return true
	}
	r, ok := ParseTitleKey(key)
	if !ok {
		return false
	}
	switch r.Kind {
	case "season":
		return len(w["series/tmdb/"+r.ID]) > 0
	case "series":
		prefix := key + "/s"
		for k, profiles := range w {
			if len(profiles) > 0 && len(k) > len(prefix) && k[:len(prefix)] == prefix {
				return true
			}
		}
	}
	return false
}

// Depart answers an app planning to remove these titles (film, season or
// series keys): each one someone wants is held until its leave date, set
// the first time it's asked about, DepartureNotice from then; once that
// date has passed, it may go. Titles nobody wants may go at once, and
// lose any hold. The answer is the hold of each title held.
func (s *Store) Depart(ctx context.Context, titles []string, wanted Wanted, now time.Time) (map[string]time.Time, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM departures WHERE seen_at < ?`, now.Add(-departureStale).Unix()); err != nil {
		return nil, err
	}
	held := map[string]time.Time{}
	for _, title := range titles {
		if !wanted.Anyone(title) {
			if _, err := tx.ExecContext(ctx, `DELETE FROM departures WHERE title = ?`, title); err != nil {
				return nil, err
			}
			continue
		}
		var leaves int64
		err := tx.QueryRowContext(ctx, `INSERT INTO departures (title, leaves_at, seen_at) VALUES (?, ?, ?)
			ON CONFLICT (title) DO UPDATE SET seen_at = excluded.seen_at RETURNING leaves_at`,
			title, now.Add(DepartureNotice).Unix(), now.Unix()).Scan(&leaves)
		if err != nil {
			return nil, err
		}
		if at := time.Unix(leaves, 0); at.After(now) {
			held[title] = at
		}
	}
	return held, tx.Commit()
}

// Departures lists the titles held for a last watch now, with their leave
// dates: those an app still asks about, whose date hasn't passed.
func (s *Store) Departures(ctx context.Context, now time.Time) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	err := s.each(ctx, func(rows *sql.Rows) error {
		var title string
		var at int64
		if err := rows.Scan(&title, &at); err != nil {
			return err
		}
		out[title] = time.Unix(at, 0)
		return nil
	}, `SELECT title, leaves_at FROM departures WHERE leaves_at > ? AND seen_at >= ?`, now.Unix(), now.Add(-departureStale).Unix())
	return out, err
}
