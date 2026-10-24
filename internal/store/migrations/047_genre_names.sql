-- TMDB's genre names in the other languages offered, as titles' texts in
-- them come: each account chooses its tastes among names in its language.
-- Kept as long as the texts.
CREATE TABLE tmdb_genre_names (
    genre      INTEGER NOT NULL,
    language   TEXT NOT NULL,
    name       TEXT NOT NULL,
    fetched_at INTEGER NOT NULL,
    PRIMARY KEY (genre, language)
) STRICT;
