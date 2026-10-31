-- A profile's audio preference: "" (the site's language), "original" (each
-- title's original language) or a language tag ("fr-CA"). Its subtitle
-- language, already kept, is now its own preference, with its region.
ALTER TABLE profiles ADD COLUMN audio_language TEXT;
