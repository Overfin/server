-- Setup links: an account an admin makes gets a link to choose its
-- password, instead of a password the admin types. "setup" is a link the
-- admin copied, "setup-sent" one Overfin emailed (opening it proves the
-- address).
CREATE TABLE email_links_new (
    id         TEXT PRIMARY KEY,
    subject    TEXT NOT NULL REFERENCES users (subject) ON DELETE CASCADE,
    link_hash  BLOB NOT NULL UNIQUE,
    purpose    TEXT NOT NULL CHECK (purpose IN ('verify', 'reset', 'setup', 'setup-sent')),
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;
INSERT INTO email_links_new SELECT id, subject, link_hash, purpose, expires_at, created_at FROM email_links;
DROP TABLE email_links;
ALTER TABLE email_links_new RENAME TO email_links;
CREATE INDEX email_links_by_subject ON email_links (subject);

-- Waiting for setup: an admin made the account, and nobody chose its
-- password yet. Unverified: the person signs in, but their address isn't
-- proven yet (a copied link, with emails on and the panel's "reminder").
ALTER TABLE users ADD COLUMN setup_pending INTEGER NOT NULL DEFAULT 0 CHECK (setup_pending IN (0, 1));
ALTER TABLE users ADD COLUMN unverified INTEGER NOT NULL DEFAULT 0 CHECK (unverified IN (0, 1));
