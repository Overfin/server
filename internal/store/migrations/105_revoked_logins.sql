-- The gateway's first check of a session (package edge) must hear of a
-- login that ends before it would have: a trigger notes every live login
-- deleted (signed out, a new password, removed by an admin…), whatever code
-- deletes it, by its cookie's hash, until it would have expired anyway. The
-- backend sends them, numbered, to its gateway.
CREATE TABLE revoked_logins (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash BLOB NOT NULL,
    expires_at INTEGER NOT NULL
) STRICT;

CREATE TRIGGER revoked_logins_delete AFTER DELETE ON logins
WHEN OLD.expires_at > unixepoch()
BEGIN
    INSERT INTO revoked_logins (token_hash, expires_at) VALUES (OLD.token_hash, OLD.expires_at);
END;
