-- Codes a screen shows to be signed in from another device (rule 69).
-- Only hashes are kept: the code's, typed elsewhere, and the secret only
-- the screen holds, which collects the sign-in once someone confirmed.
CREATE TABLE device_codes (
    code_hash   BLOB PRIMARY KEY,         -- SHA-256 of the code shown
    secret_hash BLOB NOT NULL UNIQUE,     -- SHA-256 of the screen's cookie
    device      TEXT NOT NULL,            -- "Chrome · Android", as sessions
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    subject     TEXT                      -- who confirmed it; NULL until then
) STRICT;
