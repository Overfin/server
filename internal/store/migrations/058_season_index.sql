-- A season's episodes, looked up by their season (what a profile watched
-- of it, in listings): like a series', through an index.
CREATE INDEX catalog_items_season ON catalog_items (season_id) WHERE season_id IS NOT NULL;
