-- Lossy for the new categories (the older schema can't hold them): they map
-- to their closest older category, and details are dropped.
UPDATE reports SET type = 'obstruction' WHERE type IN ('road_damage', 'construction');
UPDATE reports SET type = 'other' WHERE type = 'traffic_signal_issue';
ALTER TABLE reports DROP CONSTRAINT reports_type_check;
ALTER TABLE reports ADD CONSTRAINT reports_type_check CHECK (type IN (
    'flooded', 'road_closed', 'accident', 'vehicle_stalled', 'obstruction',
    'power_outage', 'help_needed', 'shelter', 'aid_point', 'other'
));
ALTER TABLE reports DROP COLUMN IF EXISTS details;

UPDATE announcements SET type = 'road_closure' WHERE type = 'construction';
UPDATE announcements SET type = 'general' WHERE type = 'safety_notice';
ALTER TABLE announcements DROP CONSTRAINT announcements_type_check;
ALTER TABLE announcements ADD CONSTRAINT announcements_type_check CHECK (type IN (
    'flood_warning', 'evacuation', 'road_closure', 'water_release', 'weather', 'shelter_info', 'general'
));
