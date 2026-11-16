-- The titles this server told each paired server it holds (in the libraries
-- it offers it). Only those are ever told gone: a title of a library never
-- offered, changed or removed, isn't the paired server's to hear of
-- (SECURITY.md rules 96 and 98).
CREATE TABLE peer_sent (
    peer_id   TEXT NOT NULL REFERENCES peers (id) ON DELETE CASCADE,
    title_key TEXT NOT NULL,
    PRIMARY KEY (peer_id, title_key)
) STRICT;

-- Pairings made before: nothing recorded as told yet, so their next reading
-- is a whole one (as after a change of what's offered), which records it.
INSERT INTO title_changes (title_key, at) VALUES ('item/offers', unixepoch());
UPDATE peers SET offers_seq = coalesce((SELECT seq FROM sqlite_sequence WHERE name = 'title_changes'), 0);
