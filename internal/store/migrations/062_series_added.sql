-- A series' items by when they arrived: the newest of a series' episodes
-- (listings by latest, recommendations) is then read from the index, one
-- seek per series, rather than from each of its episodes' rows. Replaces
-- the index by series alone, which this one covers.
DROP INDEX catalog_items_series;
CREATE INDEX catalog_items_series ON catalog_items (series_id, added_at) WHERE series_id IS NOT NULL;
