-- Each copy's languages, as the player lists them (internal/tracks): its
-- main audio tracks' (with their region when a track's title says it,
-- "fr-CA") and its subtitles' (forced ones aside), as JSON lists of tags.
-- A title's sources say which languages they have, a peer's too, so no
-- source is suggested without the person's language (ROADMAP.md milestone
-- 6). The library is read again whole, to fill them in: its last full
-- read is made long ago, so the next one is due now. What the index has
-- stays listed meanwhile (forgetting the reads, as migrations did before,
-- emptied every list on the site until the new read was done).
ALTER TABLE catalog_items ADD COLUMN audio_tags TEXT;
ALTER TABLE catalog_items ADD COLUMN subtitle_tags TEXT;
UPDATE meta SET value = CAST(json_set(CAST(value AS TEXT), '$.fullAt', '0001-01-01T00:00:00Z') AS BLOB)
WHERE name = 'catalog-state' AND json_valid(CAST(value AS TEXT));
