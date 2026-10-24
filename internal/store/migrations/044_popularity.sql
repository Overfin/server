-- TMDB's popularity of each title, for the title valuation. Every library
-- title is fetched again once, to fill it in.
ALTER TABLE catalog_titles ADD COLUMN popularity REAL;
DELETE FROM artwork_fetched;
