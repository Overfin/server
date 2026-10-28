-- Whether a person registered themselves with an invitation, rather than
-- being added by an admin: only those an admin added get an account
-- without an invitation of their own. Those still to confirm their
-- address are known; those who already confirmed can't be told apart, and
-- stay as they were.
ALTER TABLE users ADD COLUMN registered INTEGER NOT NULL DEFAULT 0 CHECK (registered IN (0, 1));
UPDATE users SET registered = 1 WHERE invite_code IS NOT NULL;
