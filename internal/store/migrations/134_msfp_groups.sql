-- MSFP version 0 and its groups part (msfp-go v0.4.0).
--
-- Pairing codes gain how many more servers may use one, and the group a
-- group's code invites to ('' for none). Codes made before can't be read
-- under version 0 (another layout and prefix): they go.
DELETE FROM pairing_codes;
ALTER TABLE pairing_codes ADD COLUMN uses INTEGER NOT NULL DEFAULT 1;
ALTER TABLE pairing_codes ADD COLUMN group_id TEXT NOT NULL DEFAULT '';

-- The groups this server is in (msfp.GroupRecord, JSON): their signed
-- events, mail, this server's mail key (sealed) and its settings in each.
CREATE TABLE msfp_groups (
    id     TEXT PRIMARY KEY,
    record TEXT NOT NULL
) STRICT;
