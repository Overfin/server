-- The certificates each paired server says its address serves, by the
-- SHA-256 of their public key (SPKI), as its last signed hello gave them:
-- this server's connections to it accept those as well as a publicly
-- trusted one (SECURITY.md rule 99). Space-separated; empty for none.
ALTER TABLE peers ADD COLUMN certificates TEXT NOT NULL DEFAULT '';
