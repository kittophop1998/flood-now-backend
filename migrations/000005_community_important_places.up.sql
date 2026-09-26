-- Anyone can add an important place from the app. created_by_device is the
-- anonymous device that added it (NULL = operator-curated); only that device
-- (or an operator) can edit or delete it, and it is never returned by the API.
ALTER TABLE important_places
    ADD COLUMN created_by_device text CHECK (created_by_device IS NULL OR length(created_by_device) BETWEEN 8 AND 128);
-- Per-device ownership checks and the daily create limit.
CREATE INDEX idx_important_places_device ON important_places (created_by_device, created_at)
    WHERE created_by_device IS NOT NULL;
