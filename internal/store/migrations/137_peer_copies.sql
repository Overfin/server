-- Federation on MSFP's media model (Overfin's plan, phase 7).
--
-- A paired server's films and episodes come with their copies (MSFP
-- video section 3.2): kept as received, for this server's players to
-- choose from as from its own. peer_key is the paired server's own key
-- of a title kept under another here: a series it keys by TheTVDB,
-- joined through its TMDB ID, and its episodes (NULL when the keys are
-- the same).
ALTER TABLE peer_titles ADD COLUMN copies TEXT;
ALTER TABLE peer_titles ADD COLUMN peer_key TEXT;
CREATE INDEX peer_titles_by_peer_key ON peer_titles (peer_id, peer_key) WHERE peer_key IS NOT NULL;

-- Every paired server is read whole once: what was kept of its titles
-- before had no copies.
UPDATE peers SET record = json_remove(record, '$.catalogRead.seq') WHERE json_valid(record);

-- Downloads kept for their late reports (msfp.DownloadsTx), both sides:
-- paired servers' downloads here (viewer 0), and this server's viewers'
-- on paired servers (viewer 1). The engine's records, as it writes them.
CREATE TABLE IF NOT EXISTS msfp_downloads (
    viewer INTEGER NOT NULL,
    id     TEXT NOT NULL,
    peer   TEXT NOT NULL REFERENCES peers (id) ON DELETE CASCADE,
    record TEXT NOT NULL,
    PRIMARY KEY (viewer, id)
) STRICT;
