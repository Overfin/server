-- Kids profiles go by an age (max_age), one for films and series alike,
-- instead of a rating ceiling per kind (movie_ceiling and series_ceiling,
-- left unused): set from the old ceilings once, at start (the younger of
-- their ages), until the owner picks one.
ALTER TABLE profiles ADD COLUMN max_age INTEGER;
