-- The control panel's own login, on the backend: one account, made in the
-- setup wizard, nothing in common with the site's accounts.
CREATE TABLE panel_account (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    username      TEXT NOT NULL,
    password_hash TEXT NOT NULL,   -- internal/password's hash
    changed_at    INTEGER NOT NULL
) STRICT;
