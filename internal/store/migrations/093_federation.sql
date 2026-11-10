-- Federation (ROADMAP.md milestone 6), pairing only for now. This server's
-- own key is in meta ('federation-key', its seed sealed).

-- Pairing codes this server made, until used or a day old. The secret is
-- sealed (it proves the other server got the code); its hash finds the
-- code when the other server redeems it, without the secret travelling.
CREATE TABLE pairing_codes (
    id          TEXT PRIMARY KEY,
    secret_hash BLOB NOT NULL UNIQUE,   -- SHA-256 of the secret
    secret      BLOB NOT NULL,          -- sealed, bound to the id
    note        TEXT NOT NULL,          -- the operator's, "for Marc"
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL
) STRICT;

-- The servers this one is paired with, or pairing with. "ours": the other
-- server used our code, and our operator hasn't accepted yet; "theirs": we
-- used theirs, and their operator hasn't accepted yet; "paired": both did;
-- "gone": the other server no longer answers for us (it may have ended the
-- pairing while we were away), for our operator to remove.
CREATE TABLE peers (
    id         TEXT PRIMARY KEY,
    public_key BLOB NOT NULL UNIQUE,
    address    TEXT NOT NULL,           -- its site's address, https://…
    name       TEXT NOT NULL,           -- as it calls itself
    state      TEXT NOT NULL CHECK (state IN ('ours', 'theirs', 'paired', 'gone')),
    created_at INTEGER NOT NULL,
    paired_at  INTEGER,
    seen_at    INTEGER                  -- its last signed request or answer
) STRICT;
