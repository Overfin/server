-- MSFP version 1, as decided on 2026-11-18.
--
-- A next key commitment is set only by pair or by a rotation (core
-- section 12.5): there is no trust on first use from a hello any more.
-- One that came from a hello is dropped: that peer's next change of key
-- waits for the operator to confirm it, as for a peer with none.
UPDATE peers SET next_commitment = NULL, commitment_from = '' WHERE commitment_from = 'hello';

-- A key stolen with no next key safe (core section 12.7, case 2): every
-- pairing ends, and for 30 days a hello from a server it ended with gets
-- the revocation, signed with the revoked key. revoked marks the keys of
-- those servers.
ALTER TABLE forgotten_peers ADD COLUMN revoked INTEGER NOT NULL DEFAULT 0;
