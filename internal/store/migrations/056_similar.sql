-- Similar titles come with a title's page from TMDB now: every title is
-- fetched again once (seasons keep their time: see 049).
DELETE FROM artwork_fetched;
UPDATE catalog_titles SET details_at = 0;
