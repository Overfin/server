-- The languages of each film's and episode's audio (two letters, as JSON),
-- for Overfin's matching (internal/identify): a title's original language
-- among them tells it from a remake. The library is read again whole, to
-- fill them in.
ALTER TABLE catalog_items ADD COLUMN audio_languages TEXT;
DELETE FROM meta WHERE name = 'catalog-state';
