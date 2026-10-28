-- The films and series showing their name instead of a logo, to be asked of
-- TMDB again, once (catalog.Logos, as 066's list): a title with no logo in
-- the default language, nor one without text, now takes one in the
-- server's other languages, else its original language's, before none.
-- Each title whose artwork is kept and has no logo is listed, and taken off
-- the list once checked.
INSERT OR IGNORE INTO logos_to_check (kind, tmdb)
    SELECT 'movie', substr(f.key, 12) FROM artwork_fetched f
    WHERE f.key GLOB 'movie/tmdb/[0-9]*' AND substr(f.key, 12) NOT GLOB '*[^0-9]*'
        AND NOT EXISTS (SELECT 1 FROM artwork a WHERE a.key = f.key AND a.type = 'logo')
    UNION
    SELECT 'series', substr(f.key, 13) FROM artwork_fetched f
    WHERE f.key GLOB 'series/tmdb/[0-9]*' AND substr(f.key, 13) NOT GLOB '*[^0-9]*'
        AND NOT EXISTS (SELECT 1 FROM artwork a WHERE a.key = f.key AND a.type = 'logo');
