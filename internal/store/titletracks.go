package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// TitleTracks are the audio and subtitles a profile chose while watching a
// title, by language: Audio "" when none was chosen; Subtitles nil when
// none were chosen, "" when they were turned off.
type TitleTracks struct {
	Audio     string
	Subtitles *string
}

// TitleTracks returns what a profile chose for a title (a film's, a
// series' or an item's key); nothing chosen, the zero value.
func (s *Store) TitleTracks(ctx context.Context, profileID, title string) (TitleTracks, error) {
	var audio, subtitles sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT audio, subtitles FROM title_tracks WHERE profile_id = ? AND title = ?`,
		profileID, title).Scan(&audio, &subtitles)
	if errors.Is(err, sql.ErrNoRows) {
		return TitleTracks{}, nil
	}
	if err != nil {
		return TitleTracks{}, err
	}
	t := TitleTracks{Audio: audio.String}
	if subtitles.Valid {
		t.Subtitles = &subtitles.String
	}
	return t, nil
}

// SetTitleAudio keeps the audio language a profile chose for a title.
func (s *Store) SetTitleAudio(ctx context.Context, profileID, title, language string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO title_tracks (profile_id, title, audio, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (profile_id, title) DO UPDATE SET audio = excluded.audio, updated_at = excluded.updated_at`,
		profileID, title, language, time.Now().Unix())
	return err
}

// SetTitleSubtitles keeps the subtitle language a profile chose for a
// title, "" for none.
func (s *Store) SetTitleSubtitles(ctx context.Context, profileID, title, language string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO title_tracks (profile_id, title, subtitles, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (profile_id, title) DO UPDATE SET subtitles = excluded.subtitles, updated_at = excluded.updated_at`,
		profileID, title, language, time.Now().Unix())
	return err
}

// AllTitleTracks are every choice a profile made, by title key: for its
// data's export.
func (s *Store) AllTitleTracks(ctx context.Context, profileID string) (map[string]TitleTracks, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT title, audio, subtitles FROM title_tracks WHERE profile_id = ? ORDER BY title`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]TitleTracks{}
	for rows.Next() {
		var title string
		var audio, subtitles sql.NullString
		if err := rows.Scan(&title, &audio, &subtitles); err != nil {
			return nil, err
		}
		t := TitleTracks{Audio: audio.String}
		if subtitles.Valid {
			t.Subtitles = &subtitles.String
		}
		out[title] = t
	}
	return out, rows.Err()
}
