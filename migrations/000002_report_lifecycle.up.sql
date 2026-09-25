-- Categories: road_blocked (debris/tree/landslide) becomes obstruction; new
-- categories are added alongside the existing ones.
ALTER TABLE reports DROP CONSTRAINT reports_type_check;
UPDATE reports SET type = 'obstruction' WHERE type = 'road_blocked';
ALTER TABLE reports ADD CONSTRAINT reports_type_check CHECK (type IN (
    'flooded', 'road_closed', 'accident', 'vehicle_stalled', 'obstruction',
    'power_outage', 'help_needed', 'shelter', 'aid_point', 'other'
));

-- Passability per vehicle class. Existing road reports get it derived from
-- their old passability-style severity so no information is lost.
ALTER TABLE reports
    ADD COLUMN pass_walk       text CHECK (pass_walk       IN ('passable', 'caution', 'not_recommended', 'impassable', 'unknown')),
    ADD COLUMN pass_motorcycle text CHECK (pass_motorcycle IN ('passable', 'caution', 'not_recommended', 'impassable', 'unknown')),
    ADD COLUMN pass_sedan      text CHECK (pass_sedan      IN ('passable', 'caution', 'not_recommended', 'impassable', 'unknown')),
    ADD COLUMN pass_suv_pickup text CHECK (pass_suv_pickup IN ('passable', 'caution', 'not_recommended', 'impassable', 'unknown'));

UPDATE reports SET
    pass_walk       = CASE severity WHEN 'passable' THEN 'passable' WHEN 'caution' THEN 'caution' WHEN 'small_vehicle_not_recommended' THEN 'caution'         ELSE 'impassable' END,
    pass_motorcycle = CASE severity WHEN 'passable' THEN 'passable' WHEN 'caution' THEN 'caution' WHEN 'small_vehicle_not_recommended' THEN 'not_recommended' ELSE 'impassable' END,
    pass_sedan      = CASE severity WHEN 'passable' THEN 'passable' WHEN 'caution' THEN 'caution' WHEN 'small_vehicle_not_recommended' THEN 'not_recommended' ELSE 'impassable' END,
    pass_suv_pickup = CASE severity WHEN 'passable' THEN 'passable' WHEN 'caution' THEN 'caution' WHEN 'small_vehicle_not_recommended' THEN 'caution'         ELSE 'impassable' END
WHERE type IN ('flooded', 'road_closed', 'accident', 'vehicle_stalled', 'obstruction');

-- Severity becomes a general level, independent of passability.
ALTER TABLE reports DROP CONSTRAINT reports_severity_check;
UPDATE reports SET severity = CASE severity
    WHEN 'passable' THEN 'low'
    WHEN 'caution' THEN 'moderate'
    WHEN 'small_vehicle_not_recommended' THEN 'high'
    ELSE 'critical'
END;
ALTER TABLE reports ADD CONSTRAINT reports_severity_check CHECK (severity IN ('low', 'moderate', 'high', 'critical'));

-- Approximate, body-relative water depth; backfilled from water_level_cm.
ALTER TABLE reports ADD COLUMN water_depth text CHECK (water_depth IN ('unknown', 'ankle', 'shin', 'knee', 'above_knee'));
UPDATE reports SET water_depth = CASE
    WHEN water_level_cm IS NULL THEN 'unknown'
    WHEN water_level_cm < 10 THEN 'ankle'
    WHEN water_level_cm < 30 THEN 'shin'
    WHEN water_level_cm <= 50 THEN 'knee'
    ELSE 'above_knee'
END
WHERE type = 'flooded';

-- Reserved for future road-segment / area reports; point is the only kind today.
ALTER TABLE reports ADD COLUMN geometry_type text NOT NULL DEFAULT 'point'
    CHECK (geometry_type IN ('point', 'road_segment', 'area'));

-- Lifecycle: stale_at is when an unconfirmed report becomes "possibly stale";
-- resolved_at is set once enough people report it cleared.
ALTER TABLE reports ADD COLUMN stale_at timestamptz;
UPDATE reports SET stale_at = LEAST(last_verified_at + interval '2 hours', expires_at);
ALTER TABLE reports ALTER COLUMN stale_at SET NOT NULL;
ALTER TABLE reports ADD COLUMN resolved_at timestamptz;

CREATE INDEX idx_reports_open_lat_lng ON reports (latitude, longitude) WHERE resolved_at IS NULL;
CREATE INDEX idx_reports_updated_at ON reports (updated_at);

-- Append-only report history; drives in-app notifications (and future push).
CREATE TABLE report_events (
    id          bigserial PRIMARY KEY,
    report_id   uuid NOT NULL REFERENCES reports (id) ON DELETE CASCADE,
    kind        text NOT NULL CHECK (kind IN ('created', 'confirmed', 'resolved', 'reopened')),
    -- device whose confirmation caused the event (null for creation), so a
    -- device isn't notified about its own actions
    device_id   text,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_report_events_created_at ON report_events (created_at);
CREATE INDEX idx_report_events_report_id ON report_events (report_id, created_at);

INSERT INTO report_events (report_id, kind, created_at)
SELECT id, 'created', created_at FROM reports;

-- What an anonymous device follows: an area (point + radius) or one report.
CREATE TABLE follows (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id   text NOT NULL,
    kind        text NOT NULL CHECK (kind IN ('area', 'report')),
    report_id   uuid REFERENCES reports (id) ON DELETE CASCADE,
    latitude    double precision CHECK (latitude BETWEEN -90 AND 90),
    longitude   double precision CHECK (longitude BETWEEN -180 AND 180),
    radius_m    integer CHECK (radius_m > 0),
    created_at  timestamptz NOT NULL DEFAULT now(),
    CHECK (
        (kind = 'report' AND report_id IS NOT NULL AND latitude IS NULL AND longitude IS NULL AND radius_m IS NULL) OR
        (kind = 'area' AND report_id IS NULL AND latitude IS NOT NULL AND longitude IS NOT NULL AND radius_m IS NOT NULL)
    )
);
CREATE INDEX idx_follows_device_id ON follows (device_id);
CREATE UNIQUE INDEX idx_follows_device_report ON follows (device_id, report_id) WHERE kind = 'report';
