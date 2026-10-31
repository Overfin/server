package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Overfin's own matching of the libraries' films and series to TMDB titles
// (migration 072). An admin's choice (title_overrides) comes first, a
// match next, Jellyfin's last.

// MatchTarget is a film or a series to match: what its files say.
type MatchTarget struct {
	ItemID, Kind         string
	FolderName, FileName string
	RuntimeMs            int64
	// Episodes counts a series' episodes, by season.
	Episodes map[int]int
}

// MatchReason is why a match isn't sure, as identify says it.
type MatchReason struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

// MatchCandidate is a TMDB title a match weighed.
type MatchCandidate struct {
	TMDB         string `json:"tmdb"`
	Name         string `json:"name"`
	Year         int    `json:"year,omitempty"`
	OriginalName string `json:"originalName,omitempty"`
	Language     string `json:"language,omitempty"`
	Score        int    `json:"score"`
}

// Match is what matching found for an item.
type Match struct {
	TMDB       string // "" for none
	Sure       bool
	Reasons    []MatchReason
	Candidates []MatchCandidate
}

// ToMatch lists up to limit films and series to match: those no admin
// chose a title for, never matched, or whose folder or file name changed
// since.
func (s *Store) ToMatch(ctx context.Context, limit int) ([]MatchTarget, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT i.id, i.kind, coalesce(i.folder_name, ''), coalesce(i.file_name, ''),
			coalesce(i.runtime_ms, 0)
		FROM catalog_items i LEFT JOIN title_matches m ON m.item_id = i.id
		WHERE i.kind IN ('movie', 'series')
			AND NOT EXISTS (SELECT 1 FROM title_overrides o WHERE o.item_id = i.id)
			AND (m.item_id IS NULL OR m.folder_name IS NOT i.folder_name OR (i.kind = 'movie' AND m.file_name IS NOT i.file_name))
		ORDER BY i.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	var out []MatchTarget
	for rows.Next() {
		var t MatchTarget
		if err := rows.Scan(&t.ItemID, &t.Kind, &t.FolderName, &t.FileName, &t.RuntimeMs); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Kind != "series" {
			continue
		}
		if out[i].Episodes, err = s.seasonEpisodes(ctx, out[i].ItemID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// seasonEpisodes counts a series' episodes in the libraries, by season.
func (s *Store) seasonEpisodes(ctx context.Context, seriesID string) (map[int]int, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT season_number, count(DISTINCT episode_number) FROM catalog_items
		WHERE kind = 'episode' AND series_id = ? AND season_number IS NOT NULL AND episode_number IS NOT NULL
		GROUP BY season_number`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]int{}
	for rows.Next() {
		var season, n int
		if err := rows.Scan(&season, &n); err != nil {
			return nil, err
		}
		out[season] = n
	}
	return out, rows.Err()
}

// SetMatch keeps what matching found for an item, from the names it read.
// Unless an admin chose its title, the item takes it, and what profiles
// did with its old one moves with it (as SetOverride does).
func (s *Store) SetMatch(ctx context.Context, itemID string, m Match, folderName, fileName string, at time.Time) error {
	reasons, err := json.Marshal(nonNil(m.Reasons))
	if err != nil {
		return err
	}
	candidates, err := json.Marshal(nonNil(m.Candidates))
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var kind string
	var oldKey sql.NullString
	var overridden bool
	err = tx.QueryRowContext(ctx, `SELECT kind, title_key, EXISTS (SELECT 1 FROM title_overrides WHERE item_id = id)
		FROM catalog_items WHERE id = ? AND kind IN ('movie', 'series')`, itemID).Scan(&kind, &oldKey, &overridden)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO title_matches (item_id, tmdb, sure, reasons, candidates, folder_name,
			file_name, source, matched_at) VALUES (?, ?, ?, ?, ?, ?, ?, 'overfin', ?)
		ON CONFLICT (item_id) DO UPDATE SET tmdb = excluded.tmdb, sure = excluded.sure, reasons = excluded.reasons,
			candidates = excluded.candidates, folder_name = excluded.folder_name, file_name = excluded.file_name,
			source = excluded.source, matched_at = excluded.matched_at`,
		itemID, nullString(m.TMDB), m.Sure, string(reasons), string(candidates), nullString(folderName),
		nullString(fileName), at.Unix()); err != nil {
		return err
	}
	if !overridden && m.TMDB != "" {
		if err := s.retitle(ctx, tx, itemID, kind, m.TMDB, oldKey.String); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.RefreshTitleKeys(ctx)
}

// MatchOf is what matching found for an item; false when it wasn't
// matched.
func (s *Store) MatchOf(ctx context.Context, itemID string) (Match, bool, error) {
	var m Match
	var tmdb sql.NullString
	var reasons, candidates string
	err := s.read.QueryRowContext(ctx, `SELECT tmdb, sure, reasons, candidates FROM title_matches WHERE item_id = ?`, itemID).
		Scan(&tmdb, &m.Sure, &reasons, &candidates)
	if errors.Is(err, sql.ErrNoRows) {
		return m, false, nil
	}
	if err != nil {
		return m, false, err
	}
	m.TMDB = tmdb.String
	if err := json.Unmarshal([]byte(reasons), &m.Reasons); err != nil {
		return m, false, err
	}
	if err := json.Unmarshal([]byte(candidates), &m.Candidates); err != nil {
		return m, false, err
	}
	return m, true, nil
}

// nonNil is l, or an empty list: kept as [] rather than null.
func nonNil[T any](l []T) []T {
	if l == nil {
		return []T{}
	}
	return l
}
