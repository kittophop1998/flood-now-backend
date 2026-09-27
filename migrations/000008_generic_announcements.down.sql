-- Lossy for the new values (the older schema can't hold them): new types map
-- to their closest older type, "info" to "low", and images are dropped.
ALTER TABLE announcements DROP COLUMN IF EXISTS images;

UPDATE announcements SET severity = 'low' WHERE severity = 'info';
ALTER TABLE announcements DROP CONSTRAINT announcements_severity_check;
ALTER TABLE announcements ADD CONSTRAINT announcements_severity_check CHECK (severity IN ('low', 'moderate', 'high', 'critical'));

UPDATE announcements SET type = 'road_closure' WHERE type = 'traffic_notice';
UPDATE announcements SET type = 'safety_notice' WHERE type = 'accident_emergency';
UPDATE announcements SET type = 'general' WHERE type IN ('power_utility', 'service_disruption');
ALTER TABLE announcements DROP CONSTRAINT announcements_type_check;
ALTER TABLE announcements ADD CONSTRAINT announcements_type_check CHECK (type IN (
    'flood_warning', 'evacuation', 'road_closure', 'water_release', 'weather', 'shelter_info',
    'construction', 'safety_notice', 'general'
));
