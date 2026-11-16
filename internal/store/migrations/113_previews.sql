-- Seek-bar previews Overfin makes itself (package previews), by file
-- fingerprint: each Jellyfin version's fingerprint, with the size and time
-- it was taken at (taken again when either changes), never its path
-- (rule 78); fingerprints no file has any more, and since when (their
-- previews go after 7 days); and those ffmpeg failed on, tried again later.
CREATE TABLE preview_files (
    item_id     TEXT NOT NULL,
    source_id   TEXT NOT NULL,
    size        INTEGER NOT NULL,
    modified    INTEGER NOT NULL,
    fingerprint TEXT NOT NULL,
    seen_at     INTEGER NOT NULL,
    PRIMARY KEY (item_id, source_id)
) STRICT;
CREATE INDEX preview_files_fingerprint ON preview_files (fingerprint);
CREATE TABLE previews_unused (
    fingerprint TEXT PRIMARY KEY,
    since       INTEGER NOT NULL
) STRICT;
CREATE TABLE previews_failed (
    fingerprint TEXT PRIMARY KEY,
    at          INTEGER NOT NULL
) STRICT;
