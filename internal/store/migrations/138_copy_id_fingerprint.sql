-- A copy's ID is a keyed hash of its file's fingerprint (keys.CopyID),
-- no longer of its path: a file moved or renamed keeps it, its bytes
-- replaced give another, and the same file in two places (two
-- libraries, two folders, hardlinked twins) is one copy. Each place
-- stays a row of its own, one per (item, source), so that what a
-- profile or a paired server may see is still checked by the place's
-- item; copy_id is no longer unique. place ranks the places of one copy
-- by their paths (0 for the first; the paths aren't kept): players and
-- peers get a copy through the first library in the operator's order
-- they may see, then by place.
--
-- The rows keep their old IDs until the copy index's next pass, which
-- gives each its new one (shaped again from its kept probe, unprobed).
CREATE TABLE catalog_copies_new (
    copy_id          TEXT NOT NULL,
    item_id          TEXT NOT NULL REFERENCES catalog_items (id) ON DELETE CASCADE,
    source_id        TEXT NOT NULL,
    position         INTEGER NOT NULL,
    place            INTEGER NOT NULL DEFAULT 0,
    fingerprint      TEXT NOT NULL,
    etag             TEXT NOT NULL,
    size             INTEGER NOT NULL,
    modified         INTEGER NOT NULL,
    duration_ms      INTEGER NOT NULL,
    facts            TEXT NOT NULL,
    jellyfin_streams TEXT NOT NULL,
    externals        TEXT NOT NULL DEFAULT '',
    minimal          INTEGER NOT NULL,
    parts            INTEGER NOT NULL DEFAULT 1,
    languages        TEXT,
    preview_layout   TEXT,
    probe_version    INTEGER NOT NULL DEFAULT 0,
    probed_at        INTEGER,
    served_hash      TEXT NOT NULL,
    PRIMARY KEY (item_id, source_id)
) STRICT;
INSERT INTO catalog_copies_new (copy_id, item_id, source_id, position, fingerprint, etag, size, modified, duration_ms,
        facts, jellyfin_streams, externals, minimal, parts, languages, preview_layout, probe_version, probed_at, served_hash)
    SELECT copy_id, item_id, source_id, position, fingerprint, etag, size, modified, duration_ms,
        facts, jellyfin_streams, externals, minimal, parts, languages, preview_layout, probe_version, probed_at, served_hash
    FROM catalog_copies;
DROP TABLE catalog_copies;
ALTER TABLE catalog_copies_new RENAME TO catalog_copies;
CREATE INDEX catalog_copies_fingerprint ON catalog_copies (fingerprint);
CREATE INDEX catalog_copies_copy_id ON catalog_copies (copy_id);

-- The triggers went with the old table: the same again.
CREATE TRIGGER title_changes_copy_insert AFTER INSERT ON catalog_copies
BEGIN
    INSERT INTO title_changes (title_key, at)
        SELECT DISTINCT title_key, unixepoch() FROM catalog_items
        WHERE title_key IS NOT NULL AND (id = NEW.item_id OR range_of = NEW.item_id);
END;

CREATE TRIGGER title_changes_copy_delete AFTER DELETE ON catalog_copies
BEGIN
    INSERT INTO title_changes (title_key, at)
        SELECT DISTINCT title_key, unixepoch() FROM catalog_items
        WHERE title_key IS NOT NULL AND (id = OLD.item_id OR range_of = OLD.item_id);
END;

CREATE TRIGGER title_changes_copy_update AFTER UPDATE OF served_hash, item_id, copy_id ON catalog_copies
WHEN OLD.served_hash IS NOT NEW.served_hash OR OLD.item_id IS NOT NEW.item_id OR OLD.copy_id IS NOT NEW.copy_id
BEGIN
    INSERT INTO title_changes (title_key, at)
        SELECT DISTINCT title_key, unixepoch() FROM catalog_items
        WHERE title_key IS NOT NULL AND (id IN (OLD.item_id, NEW.item_id) OR range_of IN (OLD.item_id, NEW.item_id));
END;
