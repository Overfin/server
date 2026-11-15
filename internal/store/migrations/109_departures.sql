-- Titles an app (Overtide) plans to remove, held for a last watch: a
-- film (movie/tmdb/N), a season (series/tmdb/N/sK) or a whole series
-- (series/tmdb/N). leaves_at is when the hold ends, set once, seven days
-- after the first time the app asked; seen_at, the last time it asked: a
-- departure it stops asking about is dropped (store.Departures).
CREATE TABLE departures (
    title     TEXT PRIMARY KEY,
    leaves_at INTEGER NOT NULL,
    seen_at   INTEGER NOT NULL
) STRICT;
