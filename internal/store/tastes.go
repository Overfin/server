package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Tastes are what an account says it likes, for the title valuation: its
// own data, never shown to anyone else.
type Tastes struct {
	// Prefer is "series", "movies" or "both"; "" when it didn't say.
	Prefer string
	// Genres are TMDB genre IDs.
	Genres []int
	// Kids is whether it wants kids' content (an account with a kids
	// profile does, whatever it says).
	Kids      bool
	UpdatedAt time.Time
}

// AccountTastes are what the account said; nil when it was never asked.
func (s *Store) AccountTastes(ctx context.Context, accountID string) (*Tastes, error) {
	all, err := s.tastes(ctx, `WHERE account_id = ?`, accountID)
	if err != nil || len(all) == 0 {
		return nil, err
	}
	t := all[accountID]
	return &t, nil
}

// AllTastes are what every account said, by account ID.
func (s *Store) AllTastes(ctx context.Context) (map[string]Tastes, error) {
	return s.tastes(ctx, ``)
}

func (s *Store) tastes(ctx context.Context, where string, args ...any) (map[string]Tastes, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT account_id, coalesce(prefer, ''), genres, kids, updated_at FROM account_tastes `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Tastes{}
	for rows.Next() {
		var id, genres string
		var t Tastes
		var updated int64
		if err := rows.Scan(&id, &t.Prefer, &genres, &t.Kids, &updated); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(genres), &t.Genres)
		t.UpdatedAt = time.Unix(updated, 0).UTC()
		out[id] = t
	}
	return out, rows.Err()
}

// SetAccountTastes keeps what the account says it likes; with nothing
// said, it records that it was asked.
func (s *Store) SetAccountTastes(ctx context.Context, accountID string, t Tastes, at time.Time) error {
	genres := t.Genres
	if genres == nil {
		genres = []int{}
	}
	raw, err := json.Marshal(genres)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO account_tastes (account_id, prefer, genres, kids, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (account_id) DO UPDATE SET prefer = excluded.prefer, genres = excluded.genres, kids = excluded.kids,
			updated_at = excluded.updated_at`, accountID, nullString(t.Prefer), string(raw), t.Kids, at.Unix())
	return err
}

// TastesAsked is whether the operator has people asked what they like.
func (s *Store) TastesAsked(ctx context.Context) (bool, error) {
	var v []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'tastes-asked'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return string(v) == "on", err
}

// SetTastesAsked has people asked what they like, or not.
func (s *Store) SetTastesAsked(ctx context.Context, on bool) error {
	v := "off"
	if on {
		v = "on"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('tastes-asked', ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, []byte(v))
	return err
}

// TasteGenre is a genre people may say they like.
type TasteGenre struct {
	ID   int
	Name string
}

// TasteGenres are the genres of the titles Overfin knows: those people may
// choose among, named in language when TMDB's names in it came with
// titles' texts, in TMDB's language otherwise.
func (s *Store) TasteGenres(ctx context.Context, language string) ([]TasteGenre, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT g.genre, coalesce(n.name, min(g.name)) AS name FROM catalog_title_genres g
		LEFT JOIN tmdb_genre_names n ON n.genre = g.genre AND n.language = ?
		GROUP BY g.genre ORDER BY name`, language)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TasteGenre
	for rows.Next() {
		var g TasteGenre
		if err := rows.Scan(&g.ID, &g.Name); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
