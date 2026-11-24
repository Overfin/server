-- overfin: foreign keys off
-- Profiles never have a Jellyfin user of their own: Overfin's one Jellyfin
-- account serves everyone, and a history from another Jellyfin is copied
-- in from that server, keeping nothing of it (provision.CopyIn). The
-- columns of the profiles' own users go (with the check that tied them),
-- and the record of their import with them. SQLite can't drop a UNIQUE
-- column, so the table is rebuilt, with foreign keys off for the copy (the
-- runner checks them before committing), so nothing pointing at a profile
-- is lost; its index and triggers are made again, as they were.
DROP TABLE profile_imports;

CREATE TABLE profiles_new (
    id                TEXT PRIMARY KEY,     -- prof_…
    account_id        TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,
    kind              TEXT NOT NULL CHECK (kind IN ('standard', 'kids')),
    owner             INTEGER NOT NULL CHECK (owner IN (0, 1)),
    language          TEXT,
    pin_hash          BLOB,                 -- keyed hash, never the PIN
    pin_failures      INTEGER NOT NULL DEFAULT 0,
    pin_locked        INTEGER NOT NULL DEFAULT 0 CHECK (pin_locked IN (0, 1)),
    created_at        INTEGER NOT NULL,
    pin_failed_at     INTEGER,
    pin_locked_until  INTEGER,
    movie_ceiling     TEXT,
    series_ceiling    TEXT,
    libraries         TEXT,
    blocked_tags      TEXT,
    subtitle_language TEXT,
    theme             TEXT,
    max_age           INTEGER,
    categories        TEXT,
    kids_titles       INTEGER CHECK (kids_titles IN (0, 1)),
    audio_language    TEXT,
    UNIQUE (account_id, name)
) STRICT;

INSERT INTO profiles_new (id, account_id, name, kind, owner, language, pin_hash, pin_failures,
    pin_locked, created_at, pin_failed_at, pin_locked_until, movie_ceiling, series_ceiling,
    libraries, blocked_tags, subtitle_language, theme, max_age, categories, kids_titles,
    audio_language)
SELECT id, account_id, name, kind, owner, language, pin_hash, pin_failures,
    pin_locked, created_at, pin_failed_at, pin_locked_until, movie_ceiling, series_ceiling,
    libraries, blocked_tags, subtitle_language, theme, max_age, categories, kids_titles,
    audio_language
FROM profiles;

DROP TABLE profiles;
ALTER TABLE profiles_new RENAME TO profiles;
CREATE UNIQUE INDEX profiles_one_owner ON profiles (account_id) WHERE owner = 1;

CREATE TRIGGER access_changes_profile AFTER UPDATE OF kind, max_age, movie_ceiling, series_ceiling, libraries,
    blocked_tags, categories, kids_titles ON profiles
BEGIN
    INSERT INTO access_changes (at) VALUES (unixepoch());
    DELETE FROM access_changes WHERE seq < (SELECT max(seq) FROM access_changes);
END;

CREATE TRIGGER access_changes_profile_gone AFTER DELETE ON profiles
BEGIN
    INSERT INTO access_changes (at) VALUES (unixepoch());
    DELETE FROM access_changes WHERE seq < (SELECT max(seq) FROM access_changes);
END;
