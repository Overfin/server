-- Wishes handed to Seerr, when that's where they go: Overfin is their
-- fetcher there. Each title's Seerr request, read back for its state, and
-- when each was sent (the wishes a day).
ALTER TABLE wish_states ADD COLUMN seerr_request INTEGER;
CREATE TABLE wish_sends (
    title   TEXT NOT NULL,
    sent_at INTEGER NOT NULL
) STRICT;
CREATE INDEX wish_sends_at ON wish_sends (sent_at);
