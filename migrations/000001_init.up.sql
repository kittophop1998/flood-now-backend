CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE reports (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    type              text NOT NULL CHECK (type IN ('flooded', 'road_blocked', 'vehicle_stalled', 'help_needed')),
    severity          text NOT NULL CHECK (severity IN ('passable', 'caution', 'small_vehicle_not_recommended', 'impassable')),
    latitude          double precision NOT NULL CHECK (latitude BETWEEN -90 AND 90),
    longitude         double precision NOT NULL CHECK (longitude BETWEEN -180 AND 180),
    water_level_cm    integer CHECK (water_level_cm IS NULL OR water_level_cm >= 0),
    description       text,
    image_key         text,
    people_count      integer CHECK (people_count IS NULL OR people_count >= 0),
    has_child         boolean,
    has_elderly       boolean,
    contact_phone     text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    last_verified_at  timestamptz NOT NULL DEFAULT now(),
    expires_at        timestamptz NOT NULL
);

CREATE INDEX idx_reports_expires_at ON reports (expires_at);
CREATE INDEX idx_reports_created_at ON reports (created_at);
CREATE INDEX idx_reports_lat_lng ON reports (latitude, longitude);

CREATE TABLE report_confirmations (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    report_id   uuid NOT NULL REFERENCES reports (id) ON DELETE CASCADE,
    device_id   text NOT NULL,
    status      text NOT NULL CHECK (status IN ('still_active', 'cleared')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (report_id, device_id)
);

CREATE INDEX idx_report_confirmations_report_id ON report_confirmations (report_id);
