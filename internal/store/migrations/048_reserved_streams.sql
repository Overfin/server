-- A stream reserved for each profile, an account template's setting, off
-- by default: each profile of the account owns one of its streams, and the
-- rest are shared.
ALTER TABLE account_templates ADD COLUMN reserve_streams INTEGER NOT NULL DEFAULT 0 CHECK (reserve_streams IN (0, 1));
