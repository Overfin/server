-- Overfin's plugin sends each film's and episode's video stream since
-- 0.7.0: the library is read again whole, so each copy gets its video's
-- name (114).
UPDATE meta SET value = CAST(json_set(CAST(value AS TEXT), '$.fullAt', '0001-01-01T00:00:00Z') AS BLOB)
WHERE name = 'catalog-state' AND json_valid(CAST(value AS TEXT));
