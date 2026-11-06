package store

import (
	"context"
	"strings"
	"time"
)

// OperatorListPrefix starts the names, in catalog_lists, of the operator's
// import lists (Radarr's and Sonarr's), as the app that reads them sends
// them: "operator/" and the list's name.
const OperatorListPrefix = "operator/"

// OperatorList is one of the operator's import lists: its titles, films
// and series by TMDB ID, in the list's order.
type OperatorList struct {
	Name   string
	Titles []ListTitle
}

// ListTitle is a film or a series on a list.
type ListTitle struct {
	// Kind is "movie" or "series".
	Kind string
	TMDB string
}

// SetOperatorLists replaces the operator's lists with lists. A title the
// catalog doesn't know yet goes in nameless: the picks' describer fetches
// its page, as for every pick (PicksToDescribe).
func (s *Store) SetOperatorLists(ctx context.Context, lists []OperatorList, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM catalog_lists WHERE list LIKE '`+OperatorListPrefix+`%'`); err != nil {
		return err
	}
	title, err := tx.PrepareContext(ctx, `INSERT INTO catalog_titles (kind, tmdb, name, overview, status, seen_at, sort_name, search_name)
		VALUES (?, ?, '', '', 0, ?, '', '') ON CONFLICT (kind, tmdb) DO NOTHING`)
	if err != nil {
		return err
	}
	defer title.Close()
	place, err := tx.PrepareContext(ctx, `INSERT INTO catalog_lists (list, kind, position, tmdb) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer place.Close()
	for _, l := range lists {
		positions := map[string]int{} // kind → next place
		for _, t := range l.Titles {
			if _, err := title.ExecContext(ctx, t.Kind, t.TMDB, at.Unix()); err != nil {
				return err
			}
			if _, err := place.ExecContext(ctx, OperatorListPrefix+l.Name, t.Kind, positions[t.Kind], t.TMDB); err != nil {
				return err
			}
			positions[t.Kind]++
		}
	}
	return tx.Commit()
}

// OperatorListPositions are where each title stands on each of the
// operator's lists, from 0, by kind and TMDB ID ("movie/550",
// "series/1399"), a list's films and series each from 0.
func (s *Store) OperatorListPositions(ctx context.Context) ([]map[string]int, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT list, kind, tmdb, position FROM catalog_lists
		WHERE list LIKE '`+OperatorListPrefix+`%' ORDER BY list`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]int
	last := ""
	for rows.Next() {
		var list, kind, tmdb string
		var n int
		if err := rows.Scan(&list, &kind, &tmdb, &n); err != nil {
			return nil, err
		}
		if len(out) == 0 || list != last {
			out = append(out, map[string]int{})
			last = list
		}
		out[len(out)-1][kind+"/"+tmdb] = n
	}
	return out, rows.Err()
}

// OperatorListNames are the names of the operator's lists, as sent.
func (s *Store) OperatorListNames(ctx context.Context) ([]string, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT DISTINCT list FROM catalog_lists WHERE list LIKE '`+OperatorListPrefix+`%' ORDER BY list`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, strings.TrimPrefix(name, OperatorListPrefix))
	}
	return out, rows.Err()
}
