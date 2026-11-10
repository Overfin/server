-- Each copy's languages, as the player lists them (internal/tracks): its
-- main audio tracks' (with their region when a track's title says it,
-- "fr-CA") and its subtitles' (forced ones aside), as JSON lists of tags.
-- A title's sources say which languages they have, a peer's too, so no
-- source is suggested without the person's language (ROADMAP.md milestone
-- 6). The library is read again whole, to fill them in.
ALTER TABLE catalog_items ADD COLUMN audio_tags TEXT;
ALTER TABLE catalog_items ADD COLUMN subtitle_tags TEXT;
DELETE FROM meta WHERE name = 'catalog-state';
