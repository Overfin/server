-- Federation: the titles each paired server holds in the libraries it
-- offers us and we want (ROADMAP.md milestone 6), by title key only: a
-- peer's copies are one more source of a title. Refreshed from the peer;
-- its files and Jellyfin's IDs never come here.
CREATE TABLE peer_titles (
    peer_id    TEXT NOT NULL REFERENCES peers (id) ON DELETE CASCADE,
    title_key  TEXT NOT NULL,           -- movie/tmdb/949, series/tmdb/1399/s1/e2…
    kind       TEXT NOT NULL CHECK (kind IN ('movie', 'series', 'season', 'episode')),
    library_id TEXT NOT NULL,           -- the peer's, one of those we want
    audio      TEXT,                    -- its copies' audio languages (JSON tags)
    subtitles  TEXT,                    -- and subtitles'
    PRIMARY KEY (peer_id, title_key)
) STRICT;
CREATE INDEX peer_titles_by_key ON peer_titles (title_key);

-- When each peer's titles were last read from it.
ALTER TABLE peers ADD COLUMN titles_at INTEGER;
