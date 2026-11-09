-- Categories and episode orders (/apps/v1/categories, /apps/v1/episode-orders)
-- now need a scope of their own, catalog, ticked like the others. Keys made
-- before took them with any scope: they keep them.
UPDATE app_keys SET scope = scope || ' catalog' WHERE (' ' || scope || ' ') NOT LIKE '% catalog %';
