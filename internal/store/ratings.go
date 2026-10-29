package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Overfin's own content ratings, from TMDB: each country's scales of
// certifications, each title's certifications, and the rule a kids
// profile is held to (SECURITY.md rule 28).

// RatingLevel is one certification of a country's scale.
type RatingLevel struct {
	Certification string
	Meaning       string
	// Position is TMDB's order: higher is more mature.
	Position int
}

// NotRated is the certification TMDB gives titles no board rated. It is
// never within any ceiling, and never one.
const NotRated = "NR"

// DefaultRatingsCountry is the ratings country until the operator picks one.
const DefaultRatingsCountry = "US"

// RatingsCountry is the country whose certifications apply.
func (s *Store) RatingsCountry(ctx context.Context) (string, error) {
	var v []byte
	err := s.read.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'ratings-country'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultRatingsCountry, nil
	}
	return string(v), err
}

// SetRatingsCountry changes the ratings country, and works titles' ages
// out again: they go by its ratings first.
func (s *Store) SetRatingsCountry(ctx context.Context, country string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var old []byte
	err = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'ratings-country'`).Scan(&old)
	if errors.Is(err, sql.ErrNoRows) {
		old, err = []byte(DefaultRatingsCountry), nil
	}
	if err != nil {
		return err
	}
	if string(old) == country {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('ratings-country', ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, []byte(country)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Titles' ages go by the ratings country first.
	return s.RefreshTitleAges(ctx)
}

// SetRatingScales replaces the scales of a kind (movie, series), by
// country.
func (s *Store) SetRatingScales(ctx context.Context, kind string, scales map[string][]RatingLevel) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM rating_scales WHERE kind = ?`, kind); err != nil {
		return err
	}
	for country, levels := range scales {
		for _, l := range levels {
			if _, err := tx.ExecContext(ctx, `INSERT INTO rating_scales (kind, country, certification, meaning, position)
				VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, kind, country, l.Certification, l.Meaning, l.Position); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// RatingScale is a country's scale for a kind, from least to most mature.
func (s *Store) RatingScale(ctx context.Context, kind, country string) ([]RatingLevel, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT certification, meaning, position FROM rating_scales
		WHERE kind = ? AND country = ? ORDER BY position, certification`, kind, country)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RatingLevel
	for rows.Next() {
		var l RatingLevel
		if err := rows.Scan(&l.Certification, &l.Meaning, &l.Position); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// RatingCountries lists the countries with scales for both films and
// series.
func (s *Store) RatingCountries(ctx context.Context) ([]string, error) {
	return s.ids(ctx, `SELECT country FROM rating_scales GROUP BY country
		HAVING count(DISTINCT kind) = 2 ORDER BY country`)
}

// RatedTitle is a title whose certifications Overfin keeps.
type RatedTitle struct {
	Kind string // movie, series
	TMDB string
}

// TitlesToRate returns up to limit titles, in the library or in discovery,
// whose certifications weren't fetched since before.
//
// Titles that can't be watched at home yet (as of today, YYYY-MM-DD) are
// fetched again sooner, after pendingBefore, so a new release shows soon.
func (s *Store) TitlesToRate(ctx context.Context, before, pendingBefore time.Time, today string, limit int) ([]RatedTitle, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT kind, tmdb FROM (
			SELECT kind, tmdb FROM catalog_items WHERE kind IN ('movie', 'series') AND tmdb IS NOT NULL
			UNION SELECT kind, tmdb FROM catalog_titles) t
		WHERE NOT EXISTS (SELECT 1 FROM title_ratings_fetched f
			WHERE f.kind = t.kind AND f.tmdb = t.tmdb AND f.fetched_at >= ?
			AND (f.fetched_at >= ? OR (f.at_home IS NOT NULL AND f.at_home <= ?)))
		LIMIT ?`, before.Unix(), pendingBefore.Unix(), today, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RatedTitle
	for rows.Next() {
		var t RatedTitle
		if err := rows.Scan(&t.Kind, &t.TMDB); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetTitleRatings replaces a title's certifications, by country (none:
// TMDB has none), and when it came out (see TitleRelease; "" unknown).
func (s *Store) SetTitleRatings(ctx context.Context, t RatedTitle, ratings map[string][]string, released, atHome string, at time.Time) error {
	country, err := s.RatingsCountry(ctx)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM title_ratings WHERE kind = ? AND tmdb = ?`, t.Kind, t.TMDB); err != nil {
		return err
	}
	for country, certs := range ratings {
		for _, c := range certs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO title_ratings (kind, tmdb, country, certification)
				VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`, t.Kind, t.TMDB, country, c); err != nil {
				return err
			}
		}
	}
	if err := setTitleAge(ctx, tx, t.Kind, t.TMDB, ratings, country); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO title_ratings_fetched (kind, tmdb, fetched_at, released, at_home) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (kind, tmdb) DO UPDATE SET fetched_at = excluded.fetched_at, released = excluded.released,
			at_home = excluded.at_home`, t.Kind, t.TMDB, at.Unix(), nullString(released), nullString(atHome)); err != nil {
		return err
	}
	return tx.Commit()
}

// PurgeRatings deletes the certifications not fetched again since before:
// TMDB's terms allow keeping its data six months at most.
func (s *Store) PurgeRatings(ctx context.Context, before time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM title_ratings WHERE EXISTS (SELECT 1 FROM title_ratings_fetched f
			WHERE f.kind = title_ratings.kind AND f.tmdb = title_ratings.tmdb AND f.fetched_at < ?)`,
		`DELETE FROM title_ratings_fetched WHERE fetched_at < ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, before.Unix()); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM title_ages WHERE NOT EXISTS (SELECT 1 FROM title_ratings r
		WHERE r.kind = title_ages.kind AND r.tmdb = title_ages.tmdb)`); err != nil {
		return err
	}
	return tx.Commit()
}

// RatingProgress counts the library's films and series, and those whose
// certifications were fetched.
func (s *Store) RatingProgress(ctx context.Context) (fetched, total int, err error) {
	err = s.read.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(EXISTS (SELECT 1 FROM title_ratings_fetched f
			WHERE f.kind = t.kind AND f.tmdb = t.tmdb)), 0)
		FROM (SELECT DISTINCT kind, tmdb FROM catalog_items WHERE kind IN ('movie', 'series') AND tmdb IS NOT NULL) t`).
		Scan(&total, &fetched)
	return fetched, total, err
}

// Ceiling is what a kids profile may see: titles whose age (TitleAge) is
// MaxAge at most, films and series alike (seasons and episodes by their
// series'). A title without an age is hidden.
type Ceiling struct {
	MaxAge int
}

// allowed is the rule as SQL, for the title kindExpr and tmdbExpr name.
// Anything missing fails closed.
func (c Ceiling) allowed(kindExpr, tmdbExpr string) (string, []any) {
	return `(` + tmdbExpr + ` IS NOT NULL AND EXISTS (SELECT 1 FROM title_ages a
		WHERE a.kind = ` + kindExpr + ` AND a.tmdb = ` + tmdbExpr + ` AND a.age <= ?))`, []any{c.MaxAge}
}

// itemAllowed is the rule for the catalog_items row named table: a film
// or a series by its own TMDB ID, a season or an episode by its series'.
func (c Ceiling) itemAllowed(table string) (string, []any) {
	return c.allowed(`(CASE `+table+`.kind WHEN 'movie' THEN 'movie' ELSE 'series' END)`,
		`(CASE WHEN `+table+`.kind IN ('movie', 'series') THEN `+table+`.tmdb
			ELSE (SELECT ser.tmdb FROM catalog_items ser WHERE ser.id = `+table+`.series_id AND ser.kind = 'series') END)`)
}

// TitleAllowed says whether the ceiling allows a TMDB title (movie,
// series).
func (s *Store) TitleAllowed(ctx context.Context, c Ceiling, kind, tmdb string) (bool, error) {
	cond, args := c.allowed("t.kind", "t.tmdb")
	var ok bool
	err := s.read.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM (SELECT ? AS kind, ? AS tmdb) t WHERE `+cond+`)`,
		append([]any{kind, tmdb}, args...)...).Scan(&ok)
	return ok, err
}

// keyKind and keyTMDB split a "kind/tmdb" value (TitlesAllowed's,
// TitleReleases') in SQL.
const (
	keyKind = `substr(j.value, 1, instr(j.value, '/') - 1)`
	keyTMDB = `substr(j.value, instr(j.value, '/') + 1)`
)

// TitlesAllowed is TitleAllowed for many titles at once, by "kind/tmdb"
// ("movie/550"): those the ceiling allows.
func (s *Store) TitlesAllowed(ctx context.Context, c Ceiling, keys []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(keys) == 0 {
		return out, nil
	}
	cond, args := c.allowed(keyKind, keyTMDB)
	rows, err := s.read.QueryContext(ctx, `SELECT j.value FROM json_each(?) j WHERE `+cond,
		append([]any{jsonList(keys)}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// Release is when a title came out, as TitleRelease gives it.
type Release struct {
	Released, AtHome string
}

// TitleReleases is TitleRelease for many titles at once, by "kind/tmdb":
// those not fetched yet are left out.
func (s *Store) TitleReleases(ctx context.Context, keys []string) (map[string]Release, error) {
	out := map[string]Release{}
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := s.read.QueryContext(ctx, `SELECT j.value, r.released, r.at_home FROM json_each(?) j
		JOIN title_ratings_fetched r ON r.kind = `+keyKind+` AND r.tmdb = `+keyTMDB, jsonList(keys))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var r, h sql.NullString
		if err := rows.Scan(&k, &r, &h); err != nil {
			return nil, err
		}
		out[k] = Release{Released: r.String, AtHome: h.String}
	}
	return out, rows.Err()
}

// KidsProfiles lists every kids profile.
func (s *Store) KidsProfiles(ctx context.Context) ([]*Profile, error) {
	rows, err := s.read.QueryContext(ctx, profileSelect+` WHERE kind = 'kids'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Profile
	for rows.Next() {
		p, err := s.scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// TitleRelease returns when a film or a series came out, as last fetched:
// its first release and when it could first be watched at home
// (YYYY-MM-DD, "" when TMDB doesn't say); ok is false when it wasn't
// fetched yet.
func (s *Store) TitleRelease(ctx context.Context, kind, tmdb string) (released, atHome string, ok bool, err error) {
	var r, h sql.NullString
	err = s.read.QueryRowContext(ctx, `SELECT released, at_home FROM title_ratings_fetched WHERE kind = ? AND tmdb = ?`,
		kind, tmdb).Scan(&r, &h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return r.String, h.String, err == nil, err
}
