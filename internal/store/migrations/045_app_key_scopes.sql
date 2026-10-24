-- An app key may be for several things: its scopes, separated by spaces
-- (watch-stats, score, stats, availability). The application checks them.
CREATE TABLE app_keys_new (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    key_hash     BLOB NOT NULL UNIQUE,
    scope        TEXT NOT NULL CHECK (scope != ''),
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER
) STRICT;
INSERT INTO app_keys_new SELECT id, name, key_hash, scope, created_at, last_used_at FROM app_keys;
DROP TABLE app_keys;
ALTER TABLE app_keys_new RENAME TO app_keys;
