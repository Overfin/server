-- Federation moves to the MSFP engine (package msfp): its records live in
-- this database, as the engine's Store (store/msfp.go). This migration
-- adds where they go; the store converts what Overfin's own federation
-- kept into them in the same transaction (convertFederation, in
-- store/msfp_convert.go), and 130_msfp_engine_cleanup.sql drops what no
-- longer reads it.

-- This server's own record (msfp.ServerRecord, JSON): its keys, sealed,
-- its address and its settings. One row.
CREATE TABLE msfp_server (
    id     INTEGER PRIMARY KEY CHECK (id = 1),
    record TEXT NOT NULL
) STRICT;

-- Each peer's record (msfp.PeerRecord, JSON), but for what it offers,
-- kept in peer_offers. The columns beside it (name, state, paired_at,
-- suspended_at, public_key) stay for the pages that join peers' titles
-- with this server's own data, copied from it at each write.
ALTER TABLE peers ADD COLUMN record TEXT;

-- A paired server's titles' other IDs (MSFP video section 3, ids), as
-- JSON, as it listed them.
ALTER TABLE peer_titles ADD COLUMN ids TEXT;
