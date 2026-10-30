package store

import (
	"context"
	"database/sql"
	"time"
)

// HistoryEvent is a film or a season arriving in the libraries or
// leaving them. A season carries its series' IDs.
type HistoryEvent struct {
	ID     int64
	Event  string // arrived, left
	Kind   string // movie, season
	TMDB   string
	TVDB   string
	Season *int
	At     time.Time
}

func recordHistory(ctx context.Context, tx *sql.Tx, event, kind, itemID, seriesID string, season *int, tmdb, tvdb string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO title_history (event, kind, item_id, series_id, season, tmdb, tvdb, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, event, kind, itemID, nullString(seriesID), season, nullString(tmdb), nullString(tvdb), at.Unix())
	return err
}

// TitleHistory returns up to limit events since then (by when they
// happened), event only when it isn't "", after the event after (0 for
// the first page), in the order they were recorded. A season that arrived
// before its series was indexed takes the series' IDs from the index.
func (s *Store) TitleHistory(ctx context.Context, since time.Time, event string, after int64, limit int) ([]HistoryEvent, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT h.id, h.event, h.kind,
			coalesce(h.tmdb, s.tmdb, ''), coalesce(h.tvdb, s.tvdb, ''), h.season, h.at
		FROM title_history h LEFT JOIN catalog_items s ON s.id = h.series_id
		WHERE h.at >= ? AND h.id > ? AND (? = '' OR h.event = ?)
		ORDER BY h.id LIMIT ?`, since.Unix(), after, event, event, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryEvent
	for rows.Next() {
		var e HistoryEvent
		var season sql.NullInt64
		var at int64
		if err := rows.Scan(&e.ID, &e.Event, &e.Kind, &e.TMDB, &e.TVDB, &season, &at); err != nil {
			return nil, err
		}
		if season.Valid {
			n := int(season.Int64)
			e.Season = &n
		}
		e.At = time.Unix(at, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}
