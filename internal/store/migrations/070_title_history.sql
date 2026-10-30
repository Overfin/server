-- When each film and season arrived in the libraries and left them, for
-- apps (GET /apps/v1/history): Overswell, which adds titles, doesn't take
-- one again soon after it left. Nothing personal: what the server had, and
-- when. A season carries its series' IDs; tmdb and tvdb are kept as they
-- were, so a title that left is still known by them.
CREATE TABLE title_history (
    id        INTEGER PRIMARY KEY,
    event     TEXT NOT NULL CHECK (event IN ('arrived', 'left')),
    kind      TEXT NOT NULL CHECK (kind IN ('movie', 'season')),
    item_id   TEXT NOT NULL,
    series_id TEXT,
    season    INTEGER,
    tmdb      TEXT,
    tvdb      TEXT,
    at        INTEGER NOT NULL
) STRICT;

CREATE INDEX title_history_at ON title_history (at);

-- What the libraries have now arrived when Jellyfin added it.
INSERT INTO title_history (event, kind, item_id, series_id, season, tmdb, tvdb, at)
SELECT 'arrived', c.kind, c.id, c.series_id, c.season_number,
       CASE c.kind WHEN 'movie' THEN c.tmdb ELSE s.tmdb END,
       CASE c.kind WHEN 'movie' THEN c.tvdb ELSE s.tvdb END,
       coalesce(c.added_at, unixepoch())
FROM catalog_items c LEFT JOIN catalog_items s ON s.id = c.series_id
WHERE c.kind IN ('movie', 'season')
ORDER BY coalesce(c.added_at, unixepoch()), c.id;

-- The seasons still missing their series' IDs, filled in as the series is
-- indexed (PutCatalog).
CREATE INDEX title_history_missing ON title_history (series_id)
    WHERE kind = 'season' AND (tmdb IS NULL OR tvdb IS NULL);
