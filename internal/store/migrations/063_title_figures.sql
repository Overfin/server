-- What the valuation reads of every TMDB page (its name, rating, votes,
-- popularity and release date), in an index: the rating and what follows
-- it come after the page's details in the table (cast and all, thousands
-- of bytes), so reading them there meant reading through every page.
CREATE INDEX catalog_titles_figures ON catalog_titles (kind, tmdb, name, rating, rating_votes, popularity, release_date);
