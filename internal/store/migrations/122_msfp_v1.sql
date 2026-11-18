-- Federation speaks MSFP version 1 only (its core: MSFP-* headers, the
-- msfp-pair-1. code, HTTPS only). The pairings made before were made under
-- version 0, a test of the protocol, and never move to version 1: they are
-- dropped, with everything kept for them (offers, wants, their titles,
-- what each was told, playbacks on them), the servers whose pairing ended,
-- and the codes not used yet. Operators pair again with a new code. This
-- server's own key, and the IDs its libraries are known by, stay.
DELETE FROM remote_playbacks;
DELETE FROM peer_sent;
DELETE FROM peer_titles;
DELETE FROM peer_wants;
DELETE FROM peer_offers;
DELETE FROM peers;
DELETE FROM forgotten_peers;
DELETE FROM pairing_codes;

-- What each peer's latest signed hello listed of MSFP's core and parts
-- versions, as JSON ({"core":[1],"parts":{"catalog":[1],…}}); empty before
-- its first hello, when no part is on with it.
ALTER TABLE peers ADD COLUMN versions TEXT NOT NULL DEFAULT '';
