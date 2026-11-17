-- The IDs paired servers know our libraries by (MSFP version 0, section
-- 9.3): random, made when a library is first offered, kept for good, never
-- the media server's own ID nor computable from it. A library renamed
-- (Jellyfin then gives it another ID) keeps its public ID.
CREATE TABLE federation_libraries (
    library_id TEXT PRIMARY KEY,       -- Jellyfin's, never sent
    public_id  TEXT NOT NULL UNIQUE    -- what peers know it by
) STRICT;

-- Peers knew our libraries by Jellyfin's IDs until now: what they want of
-- us names IDs no longer offered, and their operators choose again. Their
-- next reading of our titles is a whole one, as after a change of what's
-- offered, so they drop what they had.
INSERT INTO title_changes (title_key, at) VALUES ('item/offers', unixepoch());
UPDATE peers SET offers_seq = coalesce((SELECT seq FROM sqlite_sequence WHERE name = 'title_changes'), 0);
