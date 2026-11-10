-- Federation, sharing library by library (ROADMAP.md milestone 6): what
-- this server offers each peer, and what it wants of what the peer offers.
-- Only what one offers and the other wants is ever shared.

-- Our libraries offered to a peer.
CREATE TABLE peer_offers (
    peer_id    TEXT NOT NULL REFERENCES peers (id) ON DELETE CASCADE,
    library_id TEXT NOT NULL,           -- one of ours, as the index knows it
    PRIMARY KEY (peer_id, library_id)
) STRICT;

-- The peer's libraries we want, with its name and kind as it gave them
-- when we picked them (shown while it can't be reached).
CREATE TABLE peer_wants (
    peer_id    TEXT NOT NULL REFERENCES peers (id) ON DELETE CASCADE,
    library_id TEXT NOT NULL,           -- the peer's own ID for it
    name       TEXT NOT NULL,
    kind       TEXT NOT NULL,
    PRIMARY KEY (peer_id, library_id)
) STRICT;
