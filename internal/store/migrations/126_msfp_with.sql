-- The other episodes a paired server's copy of an episode holds (MSFP
-- video, section 4.2: with), as it lists them: their keys, a JSON list,
-- for marking them all watched together once that copy is watched to its
-- end.
ALTER TABLE peer_titles ADD COLUMN with_keys TEXT;
