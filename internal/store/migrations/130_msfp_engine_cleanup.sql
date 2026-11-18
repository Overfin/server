-- overfin: foreign keys off
-- Federation on the MSFP engine (129_msfp_engine.sql): what Overfin's own
-- federation kept apart is now in the engine's records, converted in the
-- migration before. Its columns, its table of rotations and its meta
-- values go.

CREATE TABLE peers_new (
    id           TEXT PRIMARY KEY,
    public_key   BLOB NOT NULL UNIQUE,  -- its current key
    name         TEXT NOT NULL,
    state        TEXT NOT NULL CHECK (state IN ('ours', 'theirs', 'paired', 'gone')),
    paired_at    INTEGER,
    suspended_at INTEGER,               -- when it revoked its key
    record       TEXT NOT NULL          -- msfp.PeerRecord, JSON, its offers apart
) STRICT;
INSERT INTO peers_new (id, public_key, name, state, paired_at, suspended_at, record)
    SELECT id, public_key, name, state, paired_at, suspended_at, record FROM peers;
DROP TABLE peers;
ALTER TABLE peers_new RENAME TO peers;

DROP TABLE peer_rotations;

DELETE FROM meta WHERE name IN ('federation-key', 'federation-next-key', 'federation-next-commitment',
    'federation-old-key', 'federation-rotated-at', 'federation-old-revoked', 'federation-ids-key',
    'federation-revoked-key', 'federation-revoked-at', 'federation-address', 'federation-former-address',
    'federation-moved-at', 'federation-uncallable', 'federation-uncallable-address');

-- An offer change is a number of the engine's own now, never a row of the
-- change log.
DELETE FROM title_changes WHERE title_key LIKE 'item/%';
