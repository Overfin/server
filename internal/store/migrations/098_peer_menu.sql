-- Federation: the libraries taken from paired servers shown on the site
-- (ROADMAP.md milestone 6), each an entry of the menu beside ours.

-- Each wanted library's ID on this server's site, as ours have (32 hex
-- digits), kept while it's wanted; and our operator's own name for the
-- entry, NULL for the one the peer gives: "Films (B)".
ALTER TABLE peer_wants ADD COLUMN menu_id TEXT;
ALTER TABLE peer_wants ADD COLUMN label TEXT;
UPDATE peer_wants SET menu_id = lower(hex(randomblob(16)));
CREATE UNIQUE INDEX peer_wants_menu ON peer_wants (menu_id);

-- When this server first knew each peer title: when it arrived, as
-- listings sort what's new.
ALTER TABLE peer_titles ADD COLUMN added_at INTEGER;
UPDATE peer_titles SET added_at = unixepoch();
