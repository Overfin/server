-- Episodes TMDB has no picture for get a frame of their video instead
-- (Overfin's plugin takes it; catalog.EpisodeFrames), kept here by the
-- episode's title key: Overfin's own image, re-encoded on the way in.
-- id names the image (its URL), new with each frame. data is NULL while
-- none could be taken: tried again after a while (tried_at, tries).
CREATE TABLE episode_frames (
    title_key TEXT PRIMARY KEY,
    item      TEXT NOT NULL,          -- the file it was taken from: Jellyfin's item ID
    id        TEXT UNIQUE,            -- 32 lowercase hexadecimal digits, with data
    data      BLOB,
    tries     INTEGER NOT NULL DEFAULT 0,
    tried_at  INTEGER NOT NULL
) STRICT;
