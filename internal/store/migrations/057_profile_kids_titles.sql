-- Whether a profile wants kids titles in its recommendations: NULL, as its
-- account does (it has a kids profile, or said it wants kids titles); 0 or
-- 1, the profile's own choice.
ALTER TABLE profiles ADD COLUMN kids_titles INTEGER CHECK (kids_titles IN (0, 1));
