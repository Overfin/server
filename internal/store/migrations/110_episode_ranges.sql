-- A file holding several episodes (S01E01-E02) is one Jellyfin item; the
-- index keeps a row for each episode after its first, range_of naming the
-- item that holds it: what's in the libraries counts every episode, and
-- playing one plays the item (store.PutCatalog, RealItem).
ALTER TABLE catalog_items ADD COLUMN range_of TEXT;
CREATE INDEX catalog_items_range_of ON catalog_items (range_of) WHERE range_of IS NOT NULL;
