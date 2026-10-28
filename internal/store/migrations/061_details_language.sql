-- Each title's original language, as catalog_titles.details has it:
-- categories pick titles by it and the panel lists them, and reading it
-- from each page's JSON (its cast and all) took two seconds for the panel's
-- list on a large library. Kept in step with catalog_titles.details by
-- triggers. The language is kept as the JSON has it (no type, no STRICT),
-- so it compares the same.
CREATE TABLE catalog_details_language (
    kind     TEXT NOT NULL,
    tmdb     TEXT NOT NULL,
    language NOT NULL,
    PRIMARY KEY (kind, tmdb)
) WITHOUT ROWID;

INSERT INTO catalog_details_language (kind, tmdb, language)
    SELECT kind, tmdb, json_extract(details, '$.language') FROM catalog_titles
    WHERE details IS NOT NULL AND json_extract(details, '$.language') IS NOT NULL;

CREATE TRIGGER catalog_details_language_insert AFTER INSERT ON catalog_titles
    WHEN json_extract(NEW.details, '$.language') IS NOT NULL BEGIN
    INSERT OR REPLACE INTO catalog_details_language (kind, tmdb, language)
        VALUES (NEW.kind, NEW.tmdb, json_extract(NEW.details, '$.language'));
END;
CREATE TRIGGER catalog_details_language_update AFTER UPDATE OF details ON catalog_titles BEGIN
    DELETE FROM catalog_details_language WHERE kind = OLD.kind AND tmdb = OLD.tmdb;
    INSERT INTO catalog_details_language (kind, tmdb, language)
        SELECT NEW.kind, NEW.tmdb, json_extract(NEW.details, '$.language')
        WHERE json_extract(NEW.details, '$.language') IS NOT NULL;
END;
CREATE TRIGGER catalog_details_language_delete AFTER DELETE ON catalog_titles BEGIN
    DELETE FROM catalog_details_language WHERE kind = OLD.kind AND tmdb = OLD.tmdb;
END;
