-- Categories: sections of the site the operator makes from rules (films or
-- series, genres, original language, library, tag, rated at most, except
-- other categories), named in each language offered. Nothing moves on
-- disk. A category may also be a row on the home page.
CREATE TABLE categories (
    id       TEXT PRIMARY KEY,
    position INTEGER NOT NULL,
    names    TEXT NOT NULL,              -- JSON: {"en": "Kids", "fr": "Enfants"}
    rules    TEXT NOT NULL,              -- JSON: CategoryRules
    home     INTEGER NOT NULL DEFAULT 0 CHECK (home IN (0, 1))
) STRICT;
