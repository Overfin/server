-- TMDB's collections: series of films, such as a saga (catalog.Collections).
-- A film's collection comes with its page from now on; the films whose page
-- was fetched before are listed in collections_to_find, asked of TMDB once
-- each, and taken off the list (it stays empty afterwards).
ALTER TABLE catalog_titles ADD COLUMN collection INTEGER;  -- TMDB's ID; NULL for none
CREATE INDEX catalog_titles_collection ON catalog_titles (collection) WHERE collection IS NOT NULL;

-- Each collection a library's film belongs to, kept like a title's page
-- (TMDB's terms: refreshed, or deleted, within six months).
CREATE TABLE collections (
    id         INTEGER PRIMARY KEY,  -- TMDB's
    name       TEXT NOT NULL,
    overview   TEXT NOT NULL,
    poster     TEXT,                 -- TMDB image paths, checked
    backdrop   TEXT,
    parts      TEXT NOT NULL,        -- JSON: its films' TMDB IDs, in release order
    fetched_at INTEGER NOT NULL
) STRICT;

CREATE TABLE collections_to_find (
    tmdb TEXT PRIMARY KEY  -- a film's TMDB ID
) STRICT;
INSERT INTO collections_to_find (tmdb)
    SELECT tmdb FROM catalog_titles WHERE kind = 'movie' AND details_at > 0;
