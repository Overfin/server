-- A film's or a series' text-free poster (TMDB's posters without a
-- language): the picture behind its page on a screen taller than wide,
-- where a poster's own text (its title, its dates) would show through.
-- It's a type of artwork of its own, so the table is made again with it.
-- It's asked of TMDB with a title's page, or the first time the title is
-- shown without it; an empty path is "TMDB has none".
CREATE TABLE artwork_new (
    key  TEXT NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('poster', 'backdrop', 'logo', 'still', 'textless')),
    path TEXT NOT NULL,
    PRIMARY KEY (key, type)
) STRICT;
INSERT INTO artwork_new (key, type, path) SELECT key, type, path FROM artwork;
DROP TABLE artwork;
ALTER TABLE artwork_new RENAME TO artwork;
CREATE INDEX artwork_path ON artwork (path);
