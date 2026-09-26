DROP INDEX IF EXISTS idx_important_places_device;
ALTER TABLE important_places DROP COLUMN IF EXISTS created_by_device;
