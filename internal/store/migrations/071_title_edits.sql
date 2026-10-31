-- The metadata editor (the control panel): a film's or a series' texts
-- and artwork as an admin changed them, over TMDB's. Overfin's own, not
-- TMDB's data: never deleted with it, nor changed when TMDB's is fetched
-- again. They go with the TMDB title, not the file: a title identified
-- again as another starts without them, and finds them again if it comes
-- back.
--
-- language is a regional tag (fr-CA); for artwork, '*' too: every
-- language without its own. value is the text, or the image: a TMDB path
-- (/abc123.jpg) or an upload (upload:<id>, title_uploads). folded is a
-- name's search form (Fold), for search; '' otherwise.
CREATE TABLE title_edits (
    kind     TEXT NOT NULL CHECK (kind IN ('movie', 'series')),
    tmdb     TEXT NOT NULL,
    field    TEXT NOT NULL CHECK (field IN ('name', 'overview', 'poster', 'backdrop', 'logo')),
    language TEXT NOT NULL,
    value    TEXT NOT NULL,
    folded   TEXT NOT NULL DEFAULT '',
    set_at   INTEGER NOT NULL,
    PRIMARY KEY (kind, tmdb, field, language)
) STRICT;

-- The titles an image is the chosen artwork of (the images' access check).
CREATE INDEX title_edits_value ON title_edits (value);

-- Images an admin uploaded for titles, re-encoded on the way in. One is
-- deleted when no edit uses it any more.
CREATE TABLE title_uploads (
    id           TEXT PRIMARY KEY,  -- 32 lowercase hexadecimal digits
    type         TEXT NOT NULL CHECK (type IN ('poster', 'backdrop', 'logo')),
    content_type TEXT NOT NULL,
    data         BLOB NOT NULL,
    created_at   INTEGER NOT NULL
) STRICT;
