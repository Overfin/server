-- An episode found by TMDB's ID of it alone, which is unique across
-- series: the player's address carries no other (/play/e1158146/…). Where
-- its series shows it, else where TMDB's own order has it (an order an
-- admin chose may leave out an episode a file is).
CREATE INDEX catalog_episodes_tmdb ON catalog_episodes (tmdb_id) WHERE tmdb_id IS NOT NULL;
CREATE INDEX own_episodes_tmdb ON own_episodes (tmdb_id);
