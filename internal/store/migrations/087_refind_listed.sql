-- Titles of IMDb's lists found on TMDB before they were named as TMDB's
-- lists name theirs (an English name where the language has none, rather
-- than one in a script few can read): found again, once. Those found for
-- the libraries' own titles stay.
DELETE FROM tmdb_found WHERE source = 'imdb'
    AND NOT EXISTS (SELECT 1 FROM catalog_items i WHERE i.imdb = tmdb_found.external);
