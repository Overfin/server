package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// The metadata editor: a film's or a series' texts and artwork as an admin
// changed them, over TMDB's (migration 071). TitleInfos shows them
// wherever the title is shown.

// The fields an admin can change.
var (
	TextFields  = []string{"name", "overview"}
	ImageFields = []string{"poster", "backdrop", "logo"}
)

// AllLanguages is an image edit's language for every language without one
// of its own.
const AllLanguages = "*"

// How long an edited text may be, in characters.
const (
	MaxEditedName     = 300
	MaxEditedOverview = 5000
)

// ErrInvalidEdit: the edit isn't one Overfin keeps (field, language, value).
var ErrInvalidEdit = errors.New("store: invalid title edit")

// TitleEdit is one of a title's changed fields, in a language.
type TitleEdit struct {
	Field    string
	Language string // a regional tag, or AllLanguages for an image
	// Value is the text, or the image: a TMDB path or an upload
	// (UploadValue).
	Value string
	SetAt time.Time
}

// uploadPrefix starts an edit's value that is an upload.
const uploadPrefix = "upload:"

// UploadValue is the value of an image edit that is an upload.
func UploadValue(id string) string { return uploadPrefix + id }

// UploadID is the upload an image edit's value is, if it is one.
func UploadID(value string) (string, bool) {
	id, ok := strings.CutPrefix(value, uploadPrefix)
	return id, ok && len(id) == 32 && allOf(id, isLowerHex)
}

// isTMDBPath says whether path is a TMDB image's, as TMDB writes them.
func isTMDBPath(path string) bool {
	name, ext, ok := strings.Cut(strings.TrimPrefix(path, "/"), ".")
	return strings.HasPrefix(path, "/") && ok && (ext == "jpg" || ext == "png") &&
		len(name) >= 8 && len(name) <= 64 && allOf(name, isAlphanumeric)
}

// checkEdit says whether an edit is one Overfin keeps.
func checkEdit(kind, tmdb, field, language, value string) error {
	text := field == "name" || field == "overview"
	image := field == "poster" || field == "backdrop" || field == "logo"
	switch {
	case kind != "movie" && kind != "series", tmdb == "" || len(tmdb) > 20 || !allOf(tmdb, isDigit):
		return ErrInvalidEdit
	case !text && !image:
		return ErrInvalidEdit
	case language == "" || len(language) > 20 || (language == AllLanguages && !image):
		return ErrInvalidEdit
	case field == "name" && (strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > MaxEditedName):
		return ErrInvalidEdit
	case field == "overview" && (strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > MaxEditedOverview):
		return ErrInvalidEdit
	case image && !isTMDBPath(value):
		if _, ok := UploadID(value); !ok {
			return ErrInvalidEdit
		}
	}
	return nil
}

// TitleEdits lists a film's or a series' edits, by field then language.
func (s *Store) TitleEdits(ctx context.Context, kind, tmdb string) ([]TitleEdit, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT field, language, value, set_at FROM title_edits
		WHERE kind = ? AND tmdb = ? ORDER BY field, language`, kind, tmdb)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TitleEdit
	for rows.Next() {
		var e TitleEdit
		var at int64
		if err := rows.Scan(&e.Field, &e.Language, &e.Value, &at); err != nil {
			return nil, err
		}
		e.SetAt = time.Unix(at, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// SetTitleEdit keeps a field of a film or a series as an admin changed it,
// in a language. An upload must be kept first (PutTitleUpload), as the
// same type; the one it replaces goes if nothing else uses it.
func (s *Store) SetTitleEdit(ctx context.Context, kind, tmdb, field, language, value string, at time.Time) error {
	if err := checkEdit(kind, tmdb, field, language, value); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if id, ok := UploadID(value); ok {
		var typ string
		err := tx.QueryRowContext(ctx, `SELECT type FROM title_uploads WHERE id = ?`, id).Scan(&typ)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && typ != field) {
			return ErrInvalidEdit
		} else if err != nil {
			return err
		}
	}
	folded := ""
	if field == "name" {
		folded = Fold(value)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO title_edits (kind, tmdb, field, language, value, folded, set_at)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (kind, tmdb, field, language)
		DO UPDATE SET value = excluded.value, folded = excluded.folded, set_at = excluded.set_at`,
		kind, tmdb, field, language, value, folded, at.Unix()); err != nil {
		return err
	}
	if err := dropUnusedUploads(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteTitleEdit gives a field back to TMDB in a language; ErrNotFound
// when it wasn't changed.
func (s *Store) DeleteTitleEdit(ctx context.Context, kind, tmdb, field, language string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM title_edits WHERE kind = ? AND tmdb = ? AND field = ? AND language = ?`,
		kind, tmdb, field, language)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := dropUnusedUploads(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// uploadUnused is how old an upload no edit uses must be to be deleted:
// one just uploaded, still to be chosen, stays a while.
const uploadUnused = time.Hour

// dropUnusedUploads deletes the uploads no edit uses any more.
func dropUnusedUploads(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM title_uploads WHERE created_at < ?
		AND NOT EXISTS (SELECT 1 FROM title_edits e WHERE e.value = 'upload:' || title_uploads.id)`,
		time.Now().Add(-uploadUnused).Unix())
	return err
}

// TitleUpload is an image an admin uploaded, re-encoded.
type TitleUpload struct {
	Type, ContentType string
	Data              []byte
}

// PutTitleUpload keeps an uploaded image (re-encoded by the caller) under
// id, for SetTitleEdit.
func (s *Store) PutTitleUpload(ctx context.Context, id string, u TitleUpload, at time.Time) error {
	if len(id) != 32 || !allOf(id, isLowerHex) || (u.Type != "poster" && u.Type != "backdrop" && u.Type != "logo") {
		return ErrInvalidEdit
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Those uploaded and never chosen go.
	if err := dropUnusedUploads(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO title_uploads (id, type, content_type, data, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, u.Type, u.ContentType, u.Data, at.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// TitleUploadOf returns an uploaded image; false when there's none.
func (s *Store) TitleUploadOf(ctx context.Context, id string) (TitleUpload, bool, error) {
	var u TitleUpload
	err := s.read.QueryRowContext(ctx, `SELECT type, content_type, data FROM title_uploads WHERE id = ?`, id).
		Scan(&u.Type, &u.ContentType, &u.Data)
	if errors.Is(err, sql.ErrNoRows) {
		return u, false, nil
	}
	return u, err == nil, err
}

// UploadTitles lists the films and series (by key) an uploaded image is
// the artwork of.
func (s *Store) UploadTitles(ctx context.Context, id string) ([]string, error) {
	return s.ids(ctx, `SELECT DISTINCT kind || '/tmdb/' || tmdb FROM title_edits WHERE value = ? ORDER BY 1`, UploadValue(id))
}

// EditedArtwork lists the TMDB images edits use, by type then path: kept
// on disk with the library's own (catalog.LibraryArtwork).
func (s *Store) EditedArtwork(ctx context.Context) (map[string][]string, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT DISTINCT field, value FROM title_edits
		WHERE field IN ('poster', 'backdrop', 'logo') AND value NOT LIKE 'upload:%' ORDER BY field, value`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var field, path string
		if err := rows.Scan(&field, &path); err != nil {
			return nil, err
		}
		out[field] = append(out[field], path)
	}
	return out, rows.Err()
}

// applyEdits shows titles as admins changed them, in language ("" for the
// default one): films and series, and the series' name and images on its
// seasons and episodes.
func (s *Store) applyEdits(ctx context.Context, titles map[string]*TitleInfo, language string) error {
	var keys []string
	seen := map[string]bool{}
	for _, t := range titles {
		if t.Ref.TMDB() == "" {
			continue
		}
		key := t.Ref.Key()
		if t.Ref.Kind != "movie" {
			key = t.Ref.Series().Key()
		}
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	rows, err := s.read.QueryContext(ctx, `SELECT kind || '/tmdb/' || tmdb, field, language, value FROM title_edits
		WHERE kind || '/tmdb/' || tmdb IN (SELECT value FROM json_each(?))`, jsonList(keys))
	if err != nil {
		return err
	}
	type edit struct{ field, language, value string }
	edits := map[string][]edit{}
	for rows.Next() {
		var key string
		var e edit
		if err := rows.Scan(&key, &e.field, &e.language, &e.value); err != nil {
			rows.Close()
			return err
		}
		edits[key] = append(edits[key], e)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(edits) == 0 {
		return err
	}
	if language == "" {
		if _, language, _, err = s.Languages(ctx); err != nil {
			return err
		}
	}
	for _, t := range titles {
		key := t.Ref.Key()
		if t.Ref.Kind != "movie" {
			key = t.Ref.Series().Key()
		}
		// The language's own, else every language's (images only).
		fields := map[string]string{}
		for _, e := range edits[key] {
			if e.language == language || (e.language == AllLanguages && fields[e.field] == "") {
				fields[e.field] = e.value
			}
		}
		if len(fields) == 0 {
			continue
		}
		switch t.Ref.Kind {
		case "movie", "series":
			t.Name, t.Overview = or(fields["name"], t.Name), or(fields["overview"], t.Overview)
			if fields["poster"] != "" {
				// The poster chosen, on tall screens too.
				t.Poster, t.Textless = fields["poster"], ""
			}
		case "season":
			t.SeriesName = or(fields["name"], t.SeriesName)
		case "episode":
			t.SeriesName, t.Poster = or(fields["name"], t.SeriesName), or(fields["poster"], t.Poster)
		}
		t.Backdrop, t.Logo = or(fields["backdrop"], t.Backdrop), or(fields["logo"], t.Logo)
	}
	return nil
}
