-- The suggestions' share once taken from TMDB's trending and popular titles
-- is now the operator's import lists': the operator's figure stays.
UPDATE meta SET name = 'list-share' WHERE name = 'discovery-share';
