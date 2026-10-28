-- Ages: each title's, from its ratings in any country (the ratings
-- country's first, else the middle of the others'), worked out by Overfin
-- and kept here, so listings filter by it quickly.
CREATE TABLE title_ages (
    kind TEXT NOT NULL CHECK (kind IN ('movie', 'series')),
    tmdb TEXT NOT NULL,
    age  INTEGER NOT NULL,
    PRIMARY KEY (kind, tmdb)
) STRICT;
