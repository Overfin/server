-- TMDB IDs TMDB removed (a duplicate merged into another entry, which then
-- answers for the title), and the ID that took over: "" while unknown.
-- What profiles do under a removed ID's key goes to the new one's
-- (Store.CurrentKey), and a folder naming a removed ID isn't held against
-- the title it now is.
CREATE TABLE tmdb_gone (
    kind TEXT    NOT NULL CHECK (kind IN ('movie', 'series')),
    old  TEXT    NOT NULL,
    new  TEXT    NOT NULL DEFAULT '',
    at   INTEGER NOT NULL,
    PRIMARY KEY (kind, old)
) STRICT;
