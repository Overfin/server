-- Wishlists (ROADMAP: "Wishlists, replacing requests"): each profile's
-- titles to get, in its own order. Overfin takes turns between accounts,
-- then between an account's profiles, and hands the titles to a fetcher
-- (Overswell or Seerr) in that order; who wished never leaves Overfin.
CREATE TABLE wishes (
    profile_id TEXT NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    title      TEXT NOT NULL,   -- movie/tmdb/N, series/tmdb/N or series/tmdb/N/sK
    position   INTEGER NOT NULL, -- the person's order, 1 first
    added_at   INTEGER NOT NULL,
    PRIMARY KEY (profile_id, title)
) STRICT;
CREATE INDEX wishes_title ON wishes (title);

-- What the fetcher last said of a wished title, once for everyone who
-- wished it: not out yet (with its date), not found (tried again at
-- next_try) or downloading. No row: waiting.
CREATE TABLE wish_states (
    title      TEXT PRIMARY KEY,
    state      TEXT NOT NULL,
    release_on TEXT,             -- YYYY-MM-DD
    next_try   INTEGER,
    updated_at INTEGER NOT NULL
) STRICT;

-- When each account last had its turn, and which of its profiles' wish
-- went then: the next turn goes to its next profile.
CREATE TABLE wish_turns (
    account_id TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    turn_at    INTEGER NOT NULL,
    profile_id TEXT
) STRICT;

-- Wishes that arrived: the person is told once (seen), and the title
-- weighs in the score until they watched it or some time passed.
CREATE TABLE wish_arrivals (
    profile_id TEXT NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    title      TEXT NOT NULL,
    arrived_at INTEGER NOT NULL,
    seen       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (profile_id, title)
) STRICT;

-- Profiles their owner switched wishing off for (on by default).
CREATE TABLE wish_off (
    profile_id TEXT PRIMARY KEY REFERENCES profiles(id) ON DELETE CASCADE
) STRICT;
