-- Federation: what a film's or an episode's picture looks like (MSFP
-- video section 3.2), both ways. Ours: each copy's stepped height and HDR
-- formats, as JSON ({"height":2160,"hdr":["hdr10"]}), from its file's
-- video stream; paired servers are told of the best copy's, and a change
-- of it is a change of the title. The library is read again whole, to
-- fill it in (what the index has stays listed meanwhile). A paired
-- server's: the picture it sent of a title, kept with it; its titles are
-- read whole once, so that those it sent before are kept with theirs.
ALTER TABLE catalog_items ADD COLUMN picture TEXT;
ALTER TABLE peer_titles ADD COLUMN video TEXT;

DROP TRIGGER title_changes_update;
CREATE TRIGGER title_changes_update AFTER UPDATE OF title_key, kind, library_id, audio_tags, subtitle_tags, picture ON catalog_items
WHEN OLD.title_key IS NOT NEW.title_key OR OLD.kind IS NOT NEW.kind OR OLD.library_id IS NOT NEW.library_id
    OR OLD.audio_tags IS NOT NEW.audio_tags OR OLD.subtitle_tags IS NOT NEW.subtitle_tags OR OLD.picture IS NOT NEW.picture
BEGIN
    INSERT INTO title_changes (title_key, at)
        SELECT k, unixepoch() FROM (SELECT OLD.title_key AS k UNION SELECT NEW.title_key) WHERE k IS NOT NULL;
END;

UPDATE meta SET value = CAST(json_set(CAST(value AS TEXT), '$.fullAt', '0001-01-01T00:00:00Z') AS BLOB)
WHERE name = 'catalog-state' AND json_valid(CAST(value AS TEXT));
UPDATE peers SET record = json_remove(record, '$.catalogRead.seq') WHERE json_valid(record);
