-- What listings and searches read of TMDB's pages, in an index: a page's
-- details (its cast, often thousands of bytes) come before these columns
-- in the table, so reading them there meant reading through each page's
-- details, for every title listed.
CREATE INDEX catalog_titles_listing ON catalog_titles (kind, tmdb, sort_name, release_date, rating, year, search_name);
