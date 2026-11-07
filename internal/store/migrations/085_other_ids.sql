-- The other IDs TMDB gives a film or a series with its page (IMDb's,
-- TheTVDB's, Wikidata's), kept with it: for matching IMDb's data, telling
-- titles apart, and finding a title by any of them. They go with the TMDB
-- data they came with.
ALTER TABLE catalog_titles ADD COLUMN imdb TEXT;
ALTER TABLE catalog_titles ADD COLUMN tvdb TEXT;
ALTER TABLE catalog_titles ADD COLUMN wikidata TEXT;
CREATE INDEX catalog_titles_imdb ON catalog_titles (imdb) WHERE imdb IS NOT NULL;
CREATE INDEX catalog_titles_tvdb ON catalog_titles (tvdb) WHERE tvdb IS NOT NULL;

-- The IMDb ID TMDB gives each episode, by TMDB's ID of it: asked once per
-- episode (TMDB has no way to ask for many), only while IMDb's data is
-- on, and asked again within TMDB's six months like the rest of its data.
-- imdb is NULL when TMDB gives none.
CREATE TABLE episode_imdb (
    series     TEXT NOT NULL,      -- the series' TMDB ID
    tmdb_id    INTEGER NOT NULL,   -- TMDB's ID of the episode
    imdb       TEXT,
    checked_at INTEGER NOT NULL,
    PRIMARY KEY (series, tmdb_id)
) STRICT;

-- Every library title is fetched again once, at the usual pace, for its IDs.
UPDATE artwork_fetched SET fetched_at = 0 WHERE key LIKE 'movie/%' OR key LIKE 'series/%';
