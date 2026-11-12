-- Federation: what changed in the catalog, numbered, so a paired server
-- asks only for what changed since it last read our titles, not the whole
-- list. Triggers note every title key whose rows come, go or change in
-- what a paired server is told (kind, library, languages), whatever code
-- writes them. Old entries are trimmed: a server asking from before the
-- oldest one kept gets the whole list again.
CREATE TABLE title_changes (
    seq       INTEGER PRIMARY KEY AUTOINCREMENT,
    title_key TEXT NOT NULL,
    at        INTEGER NOT NULL
) STRICT;

CREATE TRIGGER title_changes_insert AFTER INSERT ON catalog_items
WHEN NEW.title_key IS NOT NULL
BEGIN
    INSERT INTO title_changes (title_key, at) VALUES (NEW.title_key, unixepoch());
END;

CREATE TRIGGER title_changes_delete AFTER DELETE ON catalog_items
WHEN OLD.title_key IS NOT NULL
BEGIN
    INSERT INTO title_changes (title_key, at) VALUES (OLD.title_key, unixepoch());
END;

CREATE TRIGGER title_changes_update AFTER UPDATE OF title_key, kind, library_id, audio_tags, subtitle_tags ON catalog_items
WHEN OLD.title_key IS NOT NEW.title_key OR OLD.kind IS NOT NEW.kind OR OLD.library_id IS NOT NEW.library_id
    OR OLD.audio_tags IS NOT NEW.audio_tags OR OLD.subtitle_tags IS NOT NEW.subtitle_tags
BEGIN
    INSERT INTO title_changes (title_key, at)
        SELECT k, unixepoch() FROM (SELECT OLD.title_key AS k UNION SELECT NEW.title_key) WHERE k IS NOT NULL;
END;

-- Ours of a peer: up to which change we read its titles (0: none yet),
-- and how many times what we want of it changed (a reading under older
-- wants doesn't count). Its of us: the change we were at when what we
-- offer it last changed (asking from before gets the whole list).
ALTER TABLE peers ADD COLUMN titles_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE peers ADD COLUMN wants_gen INTEGER NOT NULL DEFAULT 0;
ALTER TABLE peers ADD COLUMN offers_seq INTEGER NOT NULL DEFAULT 0;
