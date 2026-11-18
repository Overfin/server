-- Federation: a title has changed when anything a paired server is told
-- of it changes (MSFP catalog section 3.2, video section 3), its other
-- IDs and the other episodes its file holds included, not only its kind,
-- library and languages (103_title_changes.sql). Only titles in the
-- libraries are noted: a change of a title nobody holds tells nobody
-- anything.

-- A film's or a series' IMDb and TheTVDB IDs, as TMDB gives them.
CREATE TRIGGER title_changes_ids_insert AFTER INSERT ON catalog_titles
WHEN (NEW.imdb IS NOT NULL OR NEW.tvdb IS NOT NULL)
    AND EXISTS (SELECT 1 FROM catalog_items WHERE title_key = NEW.kind || '/tmdb/' || NEW.tmdb)
BEGIN
    INSERT INTO title_changes (title_key, at) VALUES (NEW.kind || '/tmdb/' || NEW.tmdb, unixepoch());
END;

CREATE TRIGGER title_changes_ids_update AFTER UPDATE OF imdb, tvdb ON catalog_titles
WHEN (OLD.imdb IS NOT NEW.imdb OR OLD.tvdb IS NOT NEW.tvdb)
    AND EXISTS (SELECT 1 FROM catalog_items WHERE title_key = NEW.kind || '/tmdb/' || NEW.tmdb)
BEGIN
    INSERT INTO title_changes (title_key, at) VALUES (NEW.kind || '/tmdb/' || NEW.tmdb, unixepoch());
END;

CREATE TRIGGER title_changes_ids_delete AFTER DELETE ON catalog_titles
WHEN (OLD.imdb IS NOT NULL OR OLD.tvdb IS NOT NULL)
    AND EXISTS (SELECT 1 FROM catalog_items WHERE title_key = OLD.kind || '/tmdb/' || OLD.tmdb)
BEGIN
    INSERT INTO title_changes (title_key, at) VALUES (OLD.kind || '/tmdb/' || OLD.tmdb, unixepoch());
END;

-- An episode's IMDb ID, for an episode known by TMDB's ID of it.
CREATE TRIGGER title_changes_episode_imdb_insert AFTER INSERT ON episode_imdb
WHEN NEW.imdb IS NOT NULL
    AND EXISTS (SELECT 1 FROM catalog_items WHERE title_key = 'series/tmdb/' || NEW.series || '/e' || NEW.tmdb_id)
BEGIN
    INSERT INTO title_changes (title_key, at) VALUES ('series/tmdb/' || NEW.series || '/e' || NEW.tmdb_id, unixepoch());
END;

CREATE TRIGGER title_changes_episode_imdb_update AFTER UPDATE OF imdb ON episode_imdb
WHEN OLD.imdb IS NOT NEW.imdb
    AND EXISTS (SELECT 1 FROM catalog_items WHERE title_key = 'series/tmdb/' || NEW.series || '/e' || NEW.tmdb_id)
BEGIN
    INSERT INTO title_changes (title_key, at) VALUES ('series/tmdb/' || NEW.series || '/e' || NEW.tmdb_id, unixepoch());
END;

CREATE TRIGGER title_changes_episode_imdb_delete AFTER DELETE ON episode_imdb
WHEN OLD.imdb IS NOT NULL
    AND EXISTS (SELECT 1 FROM catalog_items WHERE title_key = 'series/tmdb/' || OLD.series || '/e' || OLD.tmdb_id)
BEGIN
    INSERT INTO title_changes (title_key, at) VALUES ('series/tmdb/' || OLD.series || '/e' || OLD.tmdb_id, unixepoch());
END;

-- The other episodes a file holds (video section 4.2, with): every
-- episode of a file whose rows come, go, or change their key or file.
CREATE TRIGGER title_changes_with_insert AFTER INSERT ON catalog_items
WHEN NEW.kind = 'episode' AND NEW.range_of IS NOT NULL
BEGIN
    INSERT INTO title_changes (title_key, at)
        SELECT DISTINCT title_key, unixepoch() FROM catalog_items
        WHERE title_key IS NOT NULL AND (id = NEW.range_of OR range_of = NEW.range_of);
END;

CREATE TRIGGER title_changes_with_delete AFTER DELETE ON catalog_items
WHEN OLD.kind = 'episode' AND OLD.range_of IS NOT NULL
BEGIN
    INSERT INTO title_changes (title_key, at)
        SELECT DISTINCT title_key, unixepoch() FROM catalog_items
        WHERE title_key IS NOT NULL AND (id = OLD.range_of OR range_of = OLD.range_of);
END;

CREATE TRIGGER title_changes_with_update AFTER UPDATE OF title_key, range_of ON catalog_items
WHEN NEW.kind = 'episode' AND (OLD.title_key IS NOT NEW.title_key OR OLD.range_of IS NOT NEW.range_of)
    AND (OLD.range_of IS NOT NULL OR NEW.range_of IS NOT NULL
        OR EXISTS (SELECT 1 FROM catalog_items r WHERE r.range_of = NEW.id))
BEGIN
    INSERT INTO title_changes (title_key, at)
        SELECT DISTINCT title_key, unixepoch() FROM catalog_items
        WHERE title_key IS NOT NULL
            AND (id IN (coalesce(OLD.range_of, OLD.id), coalesce(NEW.range_of, NEW.id))
                OR range_of IN (coalesce(OLD.range_of, OLD.id), coalesce(NEW.range_of, NEW.id)));
END;
