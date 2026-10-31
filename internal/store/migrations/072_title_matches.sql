-- Overfin's own matching of the libraries' films and series to TMDB titles
-- (internal/identify): what it took for each, whether it's sure, why not,
-- and the candidates it weighed. An admin's choice (title_overrides) comes
-- first; this, before Jellyfin's. Matched again only when the item's
-- folder or file name changes.
CREATE TABLE title_matches (
    item_id     TEXT PRIMARY KEY REFERENCES catalog_items(id) ON DELETE CASCADE,
    tmdb        TEXT,                       -- NULL: no title found
    sure        INTEGER NOT NULL,
    reasons     TEXT NOT NULL DEFAULT '[]', -- JSON: why it isn't sure
    candidates  TEXT NOT NULL DEFAULT '[]', -- JSON: the titles weighed, best first
    folder_name TEXT,                       -- the names it was matched from
    file_name   TEXT,
    source      TEXT NOT NULL CHECK (source IN ('overfin', 'jellyfin')),
    matched_at  INTEGER NOT NULL
) STRICT;

-- The matches Jellyfin made until now are kept as they are: Overfin
-- matches the titles that arrive from now on, and those without one.
INSERT INTO title_matches (item_id, tmdb, sure, folder_name, file_name, source, matched_at)
SELECT id, coalesce(jellyfin_tmdb, tmdb), 1, folder_name, file_name, 'jellyfin', unixepoch()
FROM catalog_items
WHERE kind IN ('movie', 'series') AND coalesce(jellyfin_tmdb, tmdb) IS NOT NULL;
