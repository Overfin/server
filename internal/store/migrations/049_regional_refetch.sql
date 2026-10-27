-- In a regional language (fr-CA), TMDB answers with that region's
-- translation only: titles without one were kept with their original name,
-- no overview, and an English logo. Overfin now fills those from the
-- language alone; everything is fetched again, as when the language
-- changes.
DELETE FROM artwork_fetched;
UPDATE catalog_titles SET details_at = 0;
UPDATE catalog_seasons SET fetched_at = 0;
