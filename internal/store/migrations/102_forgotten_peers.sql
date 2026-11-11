-- Federation: the servers a pairing ended with, by a hash of their key, so
-- their requests still get a signed "unknown" (a stranger's get the plain
-- 404): nobody between two servers can fake one.
CREATE TABLE forgotten_peers (
    key_hash     BLOB PRIMARY KEY,   -- SHA-256 of the public key
    forgotten_at INTEGER NOT NULL
) STRICT;
