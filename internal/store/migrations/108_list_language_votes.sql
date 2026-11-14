-- Each title's original language and TMDB vote count as TMDB's lists and
-- searches carry them, so a title in a language the server doesn't speak
-- enters trending, popular and the picks only with enough votes (store.
-- Crossing), its page fetched or not. NULL until a list carries them.
ALTER TABLE catalog_titles ADD COLUMN list_language TEXT;
ALTER TABLE catalog_titles ADD COLUMN list_votes INTEGER;

-- Titles found on TMDB by IMDb ID before (IMDb's lists) whose language
-- isn't known are found again once, so they bring it: kept, they wouldn't
-- be asked again for months.
DELETE FROM tmdb_found WHERE source = 'imdb' AND tmdb IS NOT NULL
    AND NOT EXISTS (SELECT 1 FROM catalog_details_language d WHERE d.kind = tmdb_found.kind AND d.tmdb = tmdb_found.tmdb);
