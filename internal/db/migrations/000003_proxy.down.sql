-- SQLite does not support DROP COLUMN in older versions; recreate is not worth the complexity.
-- This migration is intentionally not reversible in place.
SELECT 1;
