-- Films and series a profile said it's not interested in: its
-- recommendations leave them out, and nothing else changes.
CREATE TABLE not_interested (
    profile_id TEXT NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    title      TEXT NOT NULL,   -- the film's or the series' title key
    at         INTEGER NOT NULL,
    PRIMARY KEY (profile_id, title)
) STRICT;
