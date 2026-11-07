-- The genres TMDB's lists and searches give titles, by number: what the
-- panel's genres left out are judged by for a title not described yet.
CREATE TABLE catalog_list_genres (
    kind  TEXT NOT NULL,
    tmdb  TEXT NOT NULL,
    genre INTEGER NOT NULL,
    PRIMARY KEY (kind, tmdb, genre),
    FOREIGN KEY (kind, tmdb) REFERENCES catalog_titles (kind, tmdb) ON DELETE CASCADE
) STRICT;
