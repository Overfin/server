-- Tastes, for the title valuation. Each title's genres as TMDB numbers
-- them (the same in every language), with their names in TMDB's language:
-- every library title is fetched again once, to fill them in.
CREATE TABLE catalog_title_genres (
    kind  TEXT NOT NULL,
    tmdb  TEXT NOT NULL,
    genre INTEGER NOT NULL,
    name  TEXT NOT NULL,
    PRIMARY KEY (kind, tmdb, genre),
    FOREIGN KEY (kind, tmdb) REFERENCES catalog_titles (kind, tmdb) ON DELETE CASCADE
) STRICT;
CREATE INDEX catalog_title_genres_genre ON catalog_title_genres (genre);
DELETE FROM artwork_fetched;

-- What each account says it likes: series, films or both (NULL: it
-- didn't say), TMDB genre IDs, kids' content. A row means it was asked.
CREATE TABLE account_tastes (
    account_id TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    prefer     TEXT CHECK (prefer IN ('series', 'movies', 'both')),
    genres     TEXT NOT NULL DEFAULT '[]',
    kids       INTEGER NOT NULL DEFAULT 0 CHECK (kids IN (0, 1)),
    updated_at INTEGER NOT NULL
) STRICT;
