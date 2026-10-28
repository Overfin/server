-- A title's logo in each language offered besides the default, kept with
-- its texts there: a logo is written text, so a profile reading another
-- language gets that language's logo or one without text, never the
-- default language's. '' when TMDB has none, or until the texts are read
-- again.
ALTER TABLE catalog_title_texts ADD COLUMN logo TEXT NOT NULL DEFAULT '';
CREATE INDEX catalog_title_texts_logo ON catalog_title_texts (logo) WHERE logo != '';
