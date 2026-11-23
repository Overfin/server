package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// Copy is what a title's page says of one of its copies: its audio and
// subtitle languages as the player lists them (forced subtitles aside).
type Copy struct {
	ID               string
	Audio, Subtitles []string
	// Picture is its picture, stepped (BestPicture), from the copy read
	// from its file; nil until Overfin read it.
	Picture *Picture
}

// Copies are the index's copies of ids, in the order given; those the
// index doesn't have are left out.
func (s *Store) Copies(ctx context.Context, ids []string) ([]Copy, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.read.QueryContext(ctx, `SELECT id, coalesce(audio_tags, ''), coalesce(subtitle_tags, ''), picture
		FROM catalog_items WHERE id IN (`+marks(len(ids))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[string]Copy{}
	for rows.Next() {
		var c Copy
		var audio, subtitles string
		var picture sql.NullString
		if err := rows.Scan(&c.ID, &audio, &subtitles, &picture); err != nil {
			return nil, err
		}
		if p, ok := readPicture(picture); ok {
			c.Picture = &p
		}
		json.Unmarshal([]byte(audio), &c.Audio)
		json.Unmarshal([]byte(subtitles), &c.Subtitles)
		byID[c.ID] = c
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []Copy
	for _, id := range ids {
		if c, ok := byID[id]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}
