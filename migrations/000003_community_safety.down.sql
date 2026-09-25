DROP TABLE IF EXISTS moderation_reports;
DROP TABLE IF EXISTS announcements;
DROP TABLE IF EXISTS important_places;
DROP TABLE IF EXISTS helpers;
DROP TABLE IF EXISTS sos_events;
DROP TABLE IF EXISTS sos_requests;

-- Saved places are dropped (they have no pre-000003 equivalent).
DELETE FROM follows WHERE kind = 'place';
DROP INDEX IF EXISTS idx_follows_device_kind;
CREATE INDEX idx_follows_device_id ON follows (device_id);
ALTER TABLE follows DROP CONSTRAINT follows_check;
ALTER TABLE follows ADD CONSTRAINT follows_check CHECK (
    (kind = 'report' AND report_id IS NOT NULL AND latitude IS NULL AND longitude IS NULL AND radius_m IS NULL) OR
    (kind = 'area' AND report_id IS NULL AND latitude IS NOT NULL AND longitude IS NOT NULL AND radius_m IS NOT NULL)
);
ALTER TABLE follows DROP CONSTRAINT follows_kind_check;
ALTER TABLE follows ADD CONSTRAINT follows_kind_check CHECK (kind IN ('area', 'report'));
ALTER TABLE follows
    DROP COLUMN updated_at,
    DROP COLUMN notify,
    DROP COLUMN preferred_vehicle,
    DROP COLUMN icon,
    DROP COLUMN name;

DELETE FROM report_events WHERE kind IN ('hidden', 'unhidden');
ALTER TABLE report_events DROP CONSTRAINT report_events_kind_check;
ALTER TABLE report_events ADD CONSTRAINT report_events_kind_check
    CHECK (kind IN ('created', 'confirmed', 'resolved', 'reopened'));

DROP INDEX IF EXISTS idx_reports_visible_lat_lng;
DROP INDEX IF EXISTS idx_reports_client_id;
ALTER TABLE reports
    DROP COLUMN hidden_reason,
    DROP COLUMN hidden_at,
    DROP COLUMN client_id;
