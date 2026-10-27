-- Notices: which ones were sent, so each goes once. An account's access
-- ending is noticed once per end date: given more time, it's noticed again
-- when that runs out. Deleted with the account.
CREATE TABLE notices_sent (
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL CHECK (kind IN ('expiring', 'expired', 'warned')),
    expires_at INTEGER NOT NULL,
    sent_at    INTEGER NOT NULL,
    PRIMARY KEY (account_id, kind, expires_at)
);
