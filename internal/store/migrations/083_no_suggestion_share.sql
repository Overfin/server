-- The suggestions no longer take a share from any list (TMDB's trending
-- and popular, then the operator's import lists): they're the picks by
-- value alone.
DELETE FROM meta WHERE name IN ('discovery-share', 'list-share');
