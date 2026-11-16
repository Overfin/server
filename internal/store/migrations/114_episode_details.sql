-- Each episode's TMDB rating, guest stars and who directed and wrote it,
-- from the season's answer (no request of its own), for its page; and each
-- copy's video as a neutral name ("2160p HDR · HEVC"), for film and episode
-- pages. Series are fetched again (the titles keeper, a little at a time)
-- to fill the episodes' columns; the library is read again whole for the
-- copies'.
ALTER TABLE catalog_episodes ADD COLUMN rating REAL;
ALTER TABLE catalog_episodes ADD COLUMN votes INTEGER;
ALTER TABLE catalog_episodes ADD COLUMN people TEXT;
ALTER TABLE catalog_items ADD COLUMN video TEXT;
UPDATE catalog_titles SET details_at = 0 WHERE kind = 'series' AND details_at IS NOT NULL;
UPDATE meta SET value = CAST(json_set(CAST(value AS TEXT), '$.fullAt', '0001-01-01T00:00:00Z') AS BLOB)
WHERE name = 'catalog-state' AND json_valid(CAST(value AS TEXT));
