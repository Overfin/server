-- Which login a session came from, so ending a device ends its login: it
-- would otherwise start a new session at its next request. Filled in as
-- sessions are used; a session not used since has none, and no login to end.
ALTER TABLE sessions ADD COLUMN login_id TEXT;

CREATE INDEX sessions_by_login ON sessions (login_id) WHERE login_id IS NOT NULL;
