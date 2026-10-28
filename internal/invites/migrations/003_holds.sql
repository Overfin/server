-- A registration with Overfin's own sign-in takes a use of its invitation
-- at once, held for it until its account is set up: hold_hash is the
-- SHA-256 of the hold's token, which only the registration has, and
-- held_until is when an unused hold lapses and counts no more. The
-- account's use, once it's set up, has neither.
ALTER TABLE invites_uses ADD COLUMN hold_hash TEXT;
ALTER TABLE invites_uses ADD COLUMN held_until INTEGER;
CREATE UNIQUE INDEX invites_uses_hold ON invites_uses (hold_hash);
