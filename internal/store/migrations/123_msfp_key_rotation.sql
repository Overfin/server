-- MSFP key rotation and revocation (core section 12). This server's own
-- next key, made in advance, is in meta ('federation-next-key', its seed
-- sealed, until the operator exports and deletes it), with its commitment
-- ('federation-next-commitment', public); while its key is changing, its
-- old key ('federation-old-key', sealed) and when the change began
-- ('federation-rotated-at'). 'federation-ids-key' (sealed) makes the IDs
-- peers know this server's versions by, so they stay through a change of
-- key.
--
-- For each peer, what this server knows of its keys:
--   next_commitment, commitment_from: its next key commitment, and where
--     it came from ('pair', 'rotation', 'hello' for trust on first use,
--     or '' for none ever);
--   previous_key, previous_until: its key before its latest rotation,
--     still heard until then (10 minutes);
--   pending_rotation: a rotation to a key without a commitment, waiting
--     for our operator to confirm its fingerprint (JSON, '' for none);
--   suspended_at: when it revoked its key;
--   refused_key, refused_at: a rotation to a key that didn't match its
--     commitment, for our operator to see;
-- and what it knows of ours:
--   our_key: '' (our current key), 'old' (our key is changing and it
--     hasn't moved yet: signed with the old one), 'lost' (it didn't move
--     before the old key was dropped: pair again);
--   rotation_answer: its latest answer to our rotate, 'pending' or
--     'refused', '' otherwise; rotate_sent_at: when we last sent one;
--   commitment_told: it holds our commitment (from pair or a rotation).
ALTER TABLE peers ADD COLUMN next_commitment BLOB;
ALTER TABLE peers ADD COLUMN commitment_from TEXT NOT NULL DEFAULT ''
    CHECK (commitment_from IN ('', 'pair', 'rotation', 'hello'));
ALTER TABLE peers ADD COLUMN previous_key BLOB;
ALTER TABLE peers ADD COLUMN previous_until INTEGER;
ALTER TABLE peers ADD COLUMN pending_rotation TEXT NOT NULL DEFAULT '';
ALTER TABLE peers ADD COLUMN suspended_at INTEGER;
ALTER TABLE peers ADD COLUMN refused_key BLOB;
ALTER TABLE peers ADD COLUMN refused_at INTEGER;
ALTER TABLE peers ADD COLUMN our_key TEXT NOT NULL DEFAULT ''
    CHECK (our_key IN ('', 'old', 'lost'));
ALTER TABLE peers ADD COLUMN rotation_answer TEXT NOT NULL DEFAULT '';
ALTER TABLE peers ADD COLUMN rotate_sent_at INTEGER;
ALTER TABLE peers ADD COLUMN commitment_told INTEGER NOT NULL DEFAULT 0;
CREATE INDEX peers_previous_key ON peers (previous_key);

-- Each peer's rotations, as they took effect here: the statement and its
-- two signatures (MSFP core section 12.3, step 5). When the pairing ends,
-- every key in it is remembered as forgotten (section 7.4).
CREATE TABLE peer_rotations (
    peer_id         TEXT NOT NULL REFERENCES peers (id) ON DELETE CASCADE,
    old_key         BLOB NOT NULL,
    new_key         BLOB NOT NULL,
    next_commitment BLOB,
    date            INTEGER NOT NULL,      -- the statement's
    old_signature   BLOB NOT NULL,
    new_signature   BLOB NOT NULL,
    at              INTEGER NOT NULL       -- when it took effect here
) STRICT;
CREATE INDEX peer_rotations_peer ON peer_rotations (peer_id);
