-- Official announcements beyond floods: more year-round types, an "info"
-- severity for notices that aren't a hazard, and optional images. Additive
-- only: no stored announcement is rewritten; legacy types stay valid.

ALTER TABLE announcements DROP CONSTRAINT announcements_type_check;
ALTER TABLE announcements ADD CONSTRAINT announcements_type_check CHECK (type IN (
    'flood_warning', 'evacuation', 'road_closure', 'water_release', 'weather', 'shelter_info',
    'construction', 'safety_notice', 'general',
    'traffic_notice', 'accident_emergency', 'power_utility', 'service_disruption'
));

ALTER TABLE announcements DROP CONSTRAINT announcements_severity_check;
ALTER TABLE announcements ADD CONSTRAINT announcements_severity_check CHECK (severity IN (
    'info', 'low', 'moderate', 'high', 'critical'
));

-- Ordered image attachments ([{ "key", "width"?, "height"? }], first = cover).
-- Keys are R2 object keys minted by POST /admin/uploads/presign; URLs are
-- derived at read time, never stored.
ALTER TABLE announcements ADD COLUMN images jsonb NOT NULL DEFAULT '[]'::jsonb
    CHECK (jsonb_typeof(images) = 'array' AND jsonb_array_length(images) <= 5);
