-- The audio and subtitles a profile chose while watching a title (a
-- series: the whole series), by language, used for that title from then
-- on over the profile's preferences. subtitles: NULL, nothing chosen; "",
-- chosen off. Private to its profile, deleted with it (SECURITY.md rule
-- 40).
CREATE TABLE title_tracks (
    profile_id TEXT NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    title      TEXT NOT NULL,   -- a film's, a series' or an item's title key
    audio      TEXT,
    subtitles  TEXT,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (profile_id, title)
) STRICT;
