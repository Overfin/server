-- Changing a server's address (MSFP core section 13). This server's own
-- address, as it last federated under it, is in meta
-- ('federation-address'); after it changed, the one before
-- ('federation-former-address'), still accepted for 30 days or until every
-- peer moved, and when it changed ('federation-moved-at').
--
-- For each peer:
--   our_address: this server's address its latest request was signed for
--     ('' before its first since this column): it has moved once that is
--     the current one;
--   new_address: an address its signed hello named, not confirmed yet by
--     a hello there answered under its key ('' for none); the stored
--     address stays until then;
--   new_address_tried: when a confirming hello was last sent there;
--   new_address_failing: when the confirmation first failed, for the
--     operator to be warned after a day.
ALTER TABLE peers ADD COLUMN our_address TEXT NOT NULL DEFAULT '';
ALTER TABLE peers ADD COLUMN new_address TEXT NOT NULL DEFAULT '';
ALTER TABLE peers ADD COLUMN new_address_tried INTEGER;
ALTER TABLE peers ADD COLUMN new_address_failing INTEGER;
