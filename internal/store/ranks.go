package store

import (
	"context"
	"database/sql"
	"errors"
)

// SetTitleRanks replaces every rank with ranks: a film's or a series' key,
// its kind ("movie" or "series") and its rank among the library's titles
// of that kind, 1 the most valued.
func (s *Store) SetTitleRanks(ctx context.Context, ranks map[string]TitleRank) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM title_ranks`); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO title_ranks (title_key, kind, rank) VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for key, r := range ranks {
		if _, err := stmt.ExecContext(ctx, key, r.Kind, r.Rank); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TitleRank is a title's kind and rank.
type TitleRank struct {
	Kind string
	Rank int
}

// RankOf is a film's or a series' rank on the server; ok is false when it
// has none (not in the libraries, or not valued yet).
func (s *Store) RankOf(ctx context.Context, key string) (r TitleRank, ok bool, err error) {
	err = s.read.QueryRowContext(ctx, `SELECT kind, rank FROM title_ranks WHERE title_key = ?`, key).Scan(&r.Kind, &r.Rank)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// RankedBefore are the library's copies of the titles of kind ranked
// before rank, by title key: to count those a profile may see.
func (s *Store) RankedBefore(ctx context.Context, kind string, rank int) (map[string][]string, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT r.title_key, c.id FROM title_ranks r
		JOIN catalog_items c ON c.title_key = r.title_key
		WHERE r.kind = ? AND r.rank < ?`, kind, rank)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var key, id string
		if err := rows.Scan(&key, &id); err != nil {
			return nil, err
		}
		out[key] = append(out[key], id)
	}
	return out, rows.Err()
}
