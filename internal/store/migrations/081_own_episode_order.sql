-- A file is the episode its numbers are in TMDB's own order, whatever order
-- the series is shown in: own_episodes keeps that order. An order an admin
-- chose only rearranges what's shown (catalog_seasons, catalog_episodes):
-- shown_season and shown_episode are where it puts a file, NULL when its
-- series has TMDB's own.
CREATE TABLE own_episodes (
    series  TEXT NOT NULL,
    season  INTEGER NOT NULL,
    episode INTEGER NOT NULL,
    tmdb_id INTEGER NOT NULL,
    PRIMARY KEY (series, season, episode)
) STRICT;
CREATE INDEX own_episodes_tmdb_id ON own_episodes (series, tmdb_id);

ALTER TABLE catalog_items ADD COLUMN shown_season INTEGER;
ALTER TABLE catalog_items ADD COLUMN shown_episode INTEGER;

INSERT INTO own_episodes (series, season, episode, tmdb_id)
    SELECT series, season, episode, tmdb_id FROM catalog_episodes
    WHERE tmdb_id IS NOT NULL AND series NOT IN (SELECT series FROM series_orders);

-- Choosing an order used to rename files and marks to the IDs at their
-- numbers in it. Marks go back to those numbers, which are the files', until
-- TMDB's own order is fetched again for these series.
UPDATE OR IGNORE profile_titles SET title = 'series/tmdb/' || e.series || '/s' || e.season || '/e' || e.episode
    FROM catalog_episodes e JOIN series_orders o ON o.series = e.series
    WHERE e.tmdb_id IS NOT NULL AND profile_titles.title = 'series/tmdb/' || e.series || '/e' || e.tmdb_id;
UPDATE catalog_items SET title_key = (SELECT 'series/tmdb/' || i.tmdb || '/s' || catalog_items.season_number || '/e' || catalog_items.episode_number
        FROM catalog_items i WHERE i.id = catalog_items.series_id)
    WHERE kind = 'episode' AND season_number IS NOT NULL AND episode_number IS NOT NULL
        AND series_id IN (SELECT i.id FROM catalog_items i JOIN series_orders o ON o.series = i.tmdb WHERE i.kind = 'series');
UPDATE artwork_fetched SET fetched_at = 0 WHERE key IN (SELECT 'series/tmdb/' || series FROM series_orders);
