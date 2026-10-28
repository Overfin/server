-- The genres each title's page lists, by name, as catalog_titles.details
-- has them: listings filter and count by them, and reading them from each
-- page's JSON (its cast and all) took up to a second and a half on a large
-- library. Kept in step with catalog_titles.details by triggers. The name
-- is kept as the JSON has it (no type, no STRICT), so it compares the same.
CREATE TABLE catalog_details_genres (
    kind TEXT NOT NULL,
    tmdb TEXT NOT NULL,
    name NOT NULL,
    PRIMARY KEY (kind, tmdb, name)
) WITHOUT ROWID;

INSERT OR IGNORE INTO catalog_details_genres (kind, tmdb, name)
    SELECT t.kind, t.tmdb, g.value
    FROM catalog_titles t, json_each(t.details, '$.genres') g
    WHERE t.details IS NOT NULL AND g.value IS NOT NULL;

CREATE TRIGGER catalog_details_genres_insert AFTER INSERT ON catalog_titles WHEN NEW.details IS NOT NULL BEGIN
    INSERT OR IGNORE INTO catalog_details_genres (kind, tmdb, name)
        SELECT NEW.kind, NEW.tmdb, g.value FROM json_each(NEW.details, '$.genres') g WHERE g.value IS NOT NULL;
END;
CREATE TRIGGER catalog_details_genres_update AFTER UPDATE OF details ON catalog_titles BEGIN
    DELETE FROM catalog_details_genres WHERE kind = OLD.kind AND tmdb = OLD.tmdb;
    INSERT OR IGNORE INTO catalog_details_genres (kind, tmdb, name)
        SELECT NEW.kind, NEW.tmdb, g.value FROM json_each(NEW.details, '$.genres') g
        WHERE NEW.details IS NOT NULL AND g.value IS NOT NULL;
END;
CREATE TRIGGER catalog_details_genres_delete AFTER DELETE ON catalog_titles BEGIN
    DELETE FROM catalog_details_genres WHERE kind = OLD.kind AND tmdb = OLD.tmdb;
END;
