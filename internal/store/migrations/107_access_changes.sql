-- What a profile may see changed: the gateway's image cache must forget the
-- access answers it keeps (docs/link-protocol.md, section 10). Triggers note
-- every change that can narrow it, whatever code makes it: a profile's
-- libraries, tags, categories, kind, ages; its account's template; a
-- template or a category changed or deleted. The backend tells its gateway
-- when the last number grows (overfin/events.go); only that number matters,
-- so older rows go.
CREATE TABLE access_changes (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    at  INTEGER NOT NULL
) STRICT;

CREATE TRIGGER access_changes_profile AFTER UPDATE OF kind, max_age, movie_ceiling, series_ceiling, libraries,
    blocked_tags, categories, kids_titles ON profiles
BEGIN
    INSERT INTO access_changes (at) VALUES (unixepoch());
    DELETE FROM access_changes WHERE seq < (SELECT max(seq) FROM access_changes);
END;

CREATE TRIGGER access_changes_profile_gone AFTER DELETE ON profiles
BEGIN
    INSERT INTO access_changes (at) VALUES (unixepoch());
    DELETE FROM access_changes WHERE seq < (SELECT max(seq) FROM access_changes);
END;

CREATE TRIGGER access_changes_account AFTER UPDATE OF template_id ON accounts
BEGIN
    INSERT INTO access_changes (at) VALUES (unixepoch());
    DELETE FROM access_changes WHERE seq < (SELECT max(seq) FROM access_changes);
END;

CREATE TRIGGER access_changes_template AFTER UPDATE ON account_templates
BEGIN
    INSERT INTO access_changes (at) VALUES (unixepoch());
    DELETE FROM access_changes WHERE seq < (SELECT max(seq) FROM access_changes);
END;

CREATE TRIGGER access_changes_template_gone AFTER DELETE ON account_templates
BEGIN
    INSERT INTO access_changes (at) VALUES (unixepoch());
    DELETE FROM access_changes WHERE seq < (SELECT max(seq) FROM access_changes);
END;

CREATE TRIGGER access_changes_category AFTER UPDATE ON categories
BEGIN
    INSERT INTO access_changes (at) VALUES (unixepoch());
    DELETE FROM access_changes WHERE seq < (SELECT max(seq) FROM access_changes);
END;

CREATE TRIGGER access_changes_category_gone AFTER DELETE ON categories
BEGIN
    INSERT INTO access_changes (at) VALUES (unixepoch());
    DELETE FROM access_changes WHERE seq < (SELECT max(seq) FROM access_changes);
END;
