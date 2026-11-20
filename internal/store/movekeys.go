package store

import (
	"context"
	"database/sql"
)

// fileTables keep, by title key, what people did with a title's files:
// moved whenever a file turns out to be another title (a correction, or
// TMDB merging two entries). profile_titles isn't among them: it adds up
// (moveKeys).
var fileTables = []struct{ table, column string }{
	{"continue_dismissed", "series"},
	{"title_tracks", "title"},
	{"departures", "title"},
	{"episode_frames", "title_key"},
}

// titleTables keep, by title key, what people asked of a TMDB title
// itself: a wish, a request, "not interested". A correction leaves them
// on the title they were made for; they move only when TMDB merges the
// title into another, the same film or series under a new ID (MoveGone).
var titleTables = []struct{ table, column string }{
	{"wishes", "title"},
	{"wish_arrivals", "title"},
	{"wish_states", "title"},
	{"wish_sends", "title"},
	{"not_interested", "title"},
	{"requests", "title"},
}

// moveKeys moves what's kept of a title's files under its key (oldKey,
// and its seasons' and episodes' below it) to newKey. What profiles did
// adds up with what they did under newKey already, the way two copies of
// one title add up (mergeMarks); in the other tables, an entry newKey has
// already is kept, and the old one goes.
func moveKeys(ctx context.Context, tx *sql.Tx, oldKey, newKey string) error {
	if oldKey == "" || newKey == "" || oldKey == newKey {
		return nil
	}
	lo, hi := keyRange(oldKey + "/")
	if err := mergeMarks(ctx, tx, `FROM profile_titles p JOIN (SELECT profile_id AS pid, title AS t, ? || substr(title, ?) AS new
			FROM profile_titles WHERE title = ? OR (title >= ? AND title < ?)) n ON n.pid = p.profile_id AND n.t = p.title`,
		newKey, len(oldKey)+1, oldKey, lo, hi); err != nil {
		return err
	}
	return moveIn(ctx, tx, fileTables, oldKey, newKey)
}

// moveIn moves the entries of tables under oldKey (and below it) to newKey;
// one newKey has already is kept, and the old one goes.
func moveIn(ctx context.Context, tx *sql.Tx, tables []struct{ table, column string }, oldKey, newKey string) error {
	lo, hi := keyRange(oldKey + "/")
	for _, k := range tables {
		if _, err := tx.ExecContext(ctx, `UPDATE OR IGNORE `+k.table+` SET `+k.column+` = ? || substr(`+k.column+`, ?)
			WHERE `+k.column+` = ? OR (`+k.column+` >= ? AND `+k.column+` < ?)`, newKey, len(oldKey)+1, oldKey, lo, hi); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+k.table+` WHERE `+k.column+` = ? OR (`+k.column+` >= ? AND `+k.column+` < ?)`,
			oldKey, lo, hi); err != nil {
			return err
		}
	}
	return nil
}
