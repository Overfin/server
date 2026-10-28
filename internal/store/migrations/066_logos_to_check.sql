-- The films and series whose logo in the default language is to be asked
-- of TMDB again, once (catalog.Logos). Until 065, a title without a logo
-- in the default language got TMDB's English one, so a French server
-- showed "FREE GUY" for "L'homme libre"; title pages are fetched again
-- only every 120 days, so those logos would have stayed for months. Each
-- title with a logo kept is listed, and taken off the list once checked;
-- the list stays empty afterwards.
CREATE TABLE logos_to_check (
    kind TEXT NOT NULL CHECK (kind IN ('movie', 'series')),
    tmdb TEXT NOT NULL,
    PRIMARY KEY (kind, tmdb)
) STRICT;
-- A film's or a series' own logo (movie/tmdb/949, series/tmdb/1399), not
-- a season's or an episode's.
INSERT INTO logos_to_check (kind, tmdb)
    SELECT 'movie', substr(key, 12) FROM artwork
    WHERE type = 'logo' AND key GLOB 'movie/tmdb/[0-9]*' AND substr(key, 12) NOT GLOB '*[^0-9]*'
    UNION
    SELECT 'series', substr(key, 13) FROM artwork
    WHERE type = 'logo' AND key GLOB 'series/tmdb/[0-9]*' AND substr(key, 13) NOT GLOB '*[^0-9]*';
