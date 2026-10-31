-- Titles' keywords, as TMDB lists them ("anime", "christmas", "based on
-- novel"): what categories, the tags hidden from a profile and "More like
-- this" go by, instead of Jellyfin's tags, which were TMDB's keywords too,
-- through Jellyfin. folded is the keyword as compared (FoldTag). Every
-- library title is fetched again once, to fill them in: marked as fetched
-- long ago (deleting the marks would forget what they say besides).
CREATE TABLE catalog_title_keywords (
    kind    TEXT NOT NULL,
    tmdb    TEXT NOT NULL,
    keyword TEXT NOT NULL,
    folded  TEXT NOT NULL,
    PRIMARY KEY (kind, tmdb, folded),
    FOREIGN KEY (kind, tmdb) REFERENCES catalog_titles (kind, tmdb) ON DELETE CASCADE
) STRICT;
CREATE INDEX catalog_title_keywords_folded ON catalog_title_keywords (folded);
UPDATE artwork_fetched SET fetched_at = 0;
