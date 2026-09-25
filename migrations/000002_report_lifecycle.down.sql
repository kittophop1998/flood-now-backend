DROP TABLE IF EXISTS follows;
DROP TABLE IF EXISTS report_events;

DROP INDEX IF EXISTS idx_reports_updated_at;
DROP INDEX IF EXISTS idx_reports_open_lat_lng;

-- Map severity back to the old passability-style values, preferring the
-- sedan passability where it exists since that's what the old scale described.
ALTER TABLE reports DROP CONSTRAINT reports_severity_check;
UPDATE reports SET severity = CASE
    WHEN pass_sedan = 'passable' THEN 'passable'
    WHEN pass_sedan = 'caution' THEN 'caution'
    WHEN pass_sedan = 'not_recommended' THEN 'small_vehicle_not_recommended'
    WHEN pass_sedan = 'impassable' THEN 'impassable'
    WHEN severity = 'low' THEN 'passable'
    WHEN severity = 'moderate' THEN 'caution'
    WHEN severity = 'high' THEN 'small_vehicle_not_recommended'
    ELSE 'impassable'
END;
ALTER TABLE reports ADD CONSTRAINT reports_severity_check CHECK (severity IN ('passable', 'caution', 'small_vehicle_not_recommended', 'impassable'));

-- Categories that didn't exist before collapse onto the closest old one.
ALTER TABLE reports DROP CONSTRAINT reports_type_check;
UPDATE reports SET type = CASE
    WHEN type IN ('obstruction', 'road_closed', 'accident', 'power_outage', 'other') THEN 'road_blocked'
    WHEN type IN ('shelter', 'aid_point') THEN 'help_needed'
    ELSE type
END;
ALTER TABLE reports ADD CONSTRAINT reports_type_check CHECK (type IN ('flooded', 'road_blocked', 'vehicle_stalled', 'help_needed'));

ALTER TABLE reports
    DROP COLUMN resolved_at,
    DROP COLUMN stale_at,
    DROP COLUMN geometry_type,
    DROP COLUMN water_depth,
    DROP COLUMN pass_suv_pickup,
    DROP COLUMN pass_sedan,
    DROP COLUMN pass_motorcycle,
    DROP COLUMN pass_walk;
