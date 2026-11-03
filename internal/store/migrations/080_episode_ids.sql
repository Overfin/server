-- Episodes by TMDB's own IDs, which stay when TMDB or another order
-- renumbers them: what profiles did goes by them (series/tmdb/1399/e63056),
-- and an episode TMDB gives no ID keeps its numbers. Every series is fetched
-- again once, to fill them in; until then its keys keep working as they are.
ALTER TABLE catalog_episodes ADD COLUMN tmdb_id INTEGER;
CREATE INDEX catalog_episodes_tmdb_id ON catalog_episodes (series, tmdb_id) WHERE tmdb_id IS NOT NULL;
CREATE INDEX profile_titles_title ON profile_titles (title);

-- The TMDB episode order (an episode group) an admin chose for a series, to
-- number its seasons and episodes as its files do; none: TMDB's own. It goes
-- with the TMDB title, like the texts an admin changed.
CREATE TABLE series_orders (
    series    TEXT PRIMARY KEY,   -- the series' TMDB ID
    group_id  TEXT NOT NULL,
    name      TEXT NOT NULL,
    chosen_at INTEGER NOT NULL
) STRICT;

UPDATE artwork_fetched SET fetched_at = 0 WHERE key LIKE 'series/%';
