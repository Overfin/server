-- Federation: a paired server's titles are written a page at a time, each
-- marked with the reading that saw it; once a reading is whole, those it
-- didn't see go.
ALTER TABLE peer_titles ADD COLUMN seen INTEGER NOT NULL DEFAULT 0;
