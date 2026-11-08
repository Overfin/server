-- Every image asked for is checked first: which titles, seasons and
-- collections it's the picture of (ImageTitles, ImageCollections). Without
-- these, each check read the whole of those tables (logos have theirs,
-- 065): most of a second per image on a large catalog, for an image
-- already in memory too.
CREATE INDEX catalog_titles_poster ON catalog_titles (poster) WHERE poster IS NOT NULL;
CREATE INDEX catalog_titles_backdrop ON catalog_titles (backdrop) WHERE backdrop IS NOT NULL;
CREATE INDEX catalog_seasons_poster ON catalog_seasons (poster) WHERE poster IS NOT NULL;
CREATE INDEX collections_poster ON collections (poster) WHERE poster IS NOT NULL;
CREATE INDEX collections_backdrop ON collections (backdrop) WHERE backdrop IS NOT NULL;
