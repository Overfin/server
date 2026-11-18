-- Servers that can't be called (MSFP core section 7.5). This server's own
-- setting is in meta ('federation-uncallable', 'on' when its operator
-- says it can't be called), with the address it then federates under
-- ('federation-uncallable-address', https://msfp-<26 characters>.invalid,
-- made once and kept). While its key changes right after it was revoked,
-- 'federation-old-revoked' is '1': peers that can't be called pull the
-- revocation from the hello answers signed with the old key.
--
-- For each peer:
--   inbound: 'yes', it can be called; 'no', it said it can't (inbound
--     false): it is never called, and what it held for us is dropped;
--     'confirming', it said it can again, and isn't called until a hello
--     at its address is answered under its key;
--   calls_failed: its latest hello answer said that its last calls to
--     this server couldn't connect (callsFailed), for the panel to show.
ALTER TABLE peers ADD COLUMN inbound TEXT NOT NULL DEFAULT 'yes'
    CHECK (inbound IN ('yes', 'no', 'confirming'));
ALTER TABLE peers ADD COLUMN calls_failed INTEGER NOT NULL DEFAULT 0;
