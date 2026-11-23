-- MSFP's media model: each version of a film or an episode (a Jellyfin
-- item and media source) as an MSFP copy (video sections 3.2 to 3.6),
-- read by Overfin itself from the file's headers (package copies).
--
-- catalog_copies: one row per (item, source). copy_id is the version ID
-- players and peers see: a keyed hash of the file's path in its library
-- (keys.CopyID), never a path nor a Jellyfin ID; etag, a keyed hash of
-- the file's fingerprint (keys.Etag). facts are the copy as MSFP shapes
-- it, without its ID, etag, previews and segments (added when served);
-- jellyfin_streams maps each MSFP track ID (a<n>, s<n>, v) to Jellyfin's
-- stream index, for every call made through Jellyfin. minimal: built
-- from Jellyfin's data (the file not probed yet, or the probe failed),
-- never played as it is. parts: a stacked film's files (2 to 16).
-- languages: the file's languages as the catalog keeps them. externals:
-- a hash of the subtitle files beside it, as Jellyfin lists them (a
-- change probes again). served_hash: a hash of the copy as served
-- (previews and segments included); its changes, and rows coming and
-- going, are changes of the title (MSFP video section 3.2).
CREATE TABLE catalog_copies (
    copy_id          TEXT PRIMARY KEY,
    item_id          TEXT NOT NULL REFERENCES catalog_items (id) ON DELETE CASCADE,
    source_id        TEXT NOT NULL,
    position         INTEGER NOT NULL,
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
    UNIQUE (item_id, source_id)
) STRICT;
CREATE INDEX catalog_copies_fingerprint ON catalog_copies (fingerprint);

-- What ffprobe said of a file, by fingerprint (hardlinked twins share
-- it), for shaping again and for the identifier: its outputs without the
-- file's name or folder, without attachments, gzipped. Never served,
-- never logged. probe_version is the probe's.
CREATE TABLE copy_probes (
    fingerprint   TEXT PRIMARY KEY,
    probe_version INTEGER NOT NULL,
    media_type    TEXT NOT NULL,
    size          INTEGER NOT NULL,
    raw           BLOB NOT NULL,
    at            INTEGER NOT NULL
) STRICT;

-- Files whose probe failed, tried again after a week.
CREATE TABLE copy_probe_failed (
    fingerprint TEXT PRIMARY KEY,
    at          INTEGER NOT NULL
) STRICT;

-- Each version's skip markers, as MSFP segments (JSON), from Jellyfin's
-- media segments through Overfin's plugin, until Overfin finds its own.
CREATE TABLE copy_segments (
    item_id   TEXT NOT NULL REFERENCES catalog_items (id) ON DELETE CASCADE,
    source_id TEXT NOT NULL,
    segments  TEXT NOT NULL,
    PRIMARY KEY (item_id, source_id)
) STRICT;

-- A copy coming, going or changing as served is a change of the titles
-- its file holds: the item's, and the episodes after its first (range_of).
-- A copy deleted with its item finds no row: the item's own deletion
-- noted it.
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
