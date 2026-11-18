-- Federation: the key revoked when this server's key was replaced (MSFP
-- core section 12.7, case 2) is sealed under a context of its own,
-- "msfp revoked key", not the old key's any more. The record is sealed
-- again in Go, after this (msfp_convert.go, resealRevoked); nothing
-- changes in the tables.
SELECT 1;
