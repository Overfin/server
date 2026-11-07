package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

// GenresLeftOut are the genres, by TMDB's numbers and for each kind, whose
// titles the server doesn't take: one no library has is never shown when
// it's in one of them (a title a library has always is). None until the
// panel says otherwise.
type GenresLeftOut struct {
	Movie  []int `json:"movie"`
	Series []int `json:"series"`
}

// Of is the genres left out of kind's titles: films', or series' (their
// seasons' and episodes' too).
func (g GenresLeftOut) Of(kind string) []int {
	if kind == "movie" {
		return g.Movie
	}
	return g.Series
}

// None says no genre is left out.
func (g GenresLeftOut) None() bool { return len(g.Movie) == 0 && len(g.Series) == 0 }

// GenresLeftOut reads them.
func (s *Store) GenresLeftOut(ctx context.Context) (GenresLeftOut, error) {
	var v []byte
	err := s.read.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'genres-left-out'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return GenresLeftOut{Movie: []int{}, Series: []int{}}, nil
	}
	out := GenresLeftOut{}
	if err == nil {
		err = json.Unmarshal(v, &out)
	}
	if out.Movie == nil {
		out.Movie = []int{}
	}
	if out.Series == nil {
		out.Series = []int{}
	}
	return out, err
}

// SetGenresLeftOut changes them, each genre once.
func (s *Store) SetGenresLeftOut(ctx context.Context, g GenresLeftOut) error {
	for _, l := range []*[]int{&g.Movie, &g.Series} {
		*l = slices.Compact(slices.Sorted(slices.Values(*l)))
		if *l == nil {
			*l = []int{}
		}
	}
	v, err := json.Marshal(g)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('genres-left-out', ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, v)
	return err
}

// leftOut is the condition that the title kind, tmdb (columns or
// expressions) is in a genre left out: its page's genres, or those a list
// gave it.
func leftOut(kind, tmdb string, g GenresLeftOut) (string, []any) {
	in := func(l []int) string {
		b, _ := json.Marshal(l)
		return string(b)
	}
	genres := `(SELECT value FROM json_each(CASE ` + kind + ` WHEN 'movie' THEN ? ELSE ? END))`
	cond := `(EXISTS (SELECT 1 FROM catalog_title_genres lg WHERE lg.kind = ` + kind + ` AND lg.tmdb = ` + tmdb + ` AND lg.genre IN ` + genres + `)
		OR EXISTS (SELECT 1 FROM catalog_list_genres lg WHERE lg.kind = ` + kind + ` AND lg.tmdb = ` + tmdb + ` AND lg.genre IN ` + genres + `))`
	m, se := in(g.Movie), in(g.Series)
	return cond, []any{m, se, m, se}
}

// TitlesLeftOut are those of the titles, by "kind/tmdb" ("series/1399"),
// in a genre left out.
func (s *Store) TitlesLeftOut(ctx context.Context, g GenresLeftOut, keys []string) (map[string]bool, error) {
	out := map[string]bool{}
	if keys = plainKeys(keys); len(keys) == 0 || g.None() {
		return out, nil
	}
	cond, args := leftOut(keyKind, keyTMDB, g)
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

// leftOutPicks is the condition that a pick (catalog_titles t) isn't in a
// genre left out, for the queries on picks.
func (s *Store) leftOutPicks(ctx context.Context) (string, []any, error) {
	g, err := s.GenresLeftOut(ctx)
	if err != nil || g.None() {
		return "", nil, err
	}
	cond, args := leftOut("t.kind", "t.tmdb", g)
	return " AND NOT " + strings.TrimSpace(cond), args, nil
}
