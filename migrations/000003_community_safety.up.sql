-- Community-safety features: saved places / watch areas (as a follow kind),
-- SOS + helpers, important places, official announcements, moderation,
-- offline idempotency. See docs/database.md.

-- Reports: offline-queue idempotency and moderation visibility.
ALTER TABLE reports
    ADD COLUMN client_id     text CHECK (client_id IS NULL OR length(client_id) BETWEEN 8 AND 64),
    ADD COLUMN hidden_at     timestamptz,
    ADD COLUMN hidden_reason text CHECK (hidden_reason IN ('auto_threshold', 'admin'));
CREATE UNIQUE INDEX idx_reports_client_id ON reports (client_id) WHERE client_id IS NOT NULL;
-- Map / aggregate / route-corridor queries only ever read open, visible reports.
CREATE INDEX idx_reports_visible_lat_lng ON reports (latitude, longitude)
    INCLUDE (severity, expires_at)
    WHERE resolved_at IS NULL AND hidden_at IS NULL;

ALTER TABLE report_events DROP CONSTRAINT report_events_kind_check;
ALTER TABLE report_events ADD CONSTRAINT report_events_kind_check
    CHECK (kind IN ('created', 'confirmed', 'resolved', 'reopened', 'hidden', 'unhidden'));

-- Saved places are a third follow kind: a named area follow whose
-- notifications can be switched off. Reusing follows keeps one
-- notification-matching query for areas, places and reports.
ALTER TABLE follows
    ADD COLUMN name              text CHECK (name IS NULL OR length(name) BETWEEN 1 AND 60),
    ADD COLUMN icon              text CHECK (icon IN ('home', 'work', 'family', 'custom')),
    ADD COLUMN preferred_vehicle text CHECK (preferred_vehicle IN ('walk', 'motorcycle', 'sedan', 'suv_pickup')),
    ADD COLUMN notify            boolean NOT NULL DEFAULT true,
    ADD COLUMN updated_at        timestamptz;
UPDATE follows SET updated_at = created_at;
ALTER TABLE follows ALTER COLUMN updated_at SET NOT NULL;
ALTER TABLE follows ALTER COLUMN updated_at SET DEFAULT now();

ALTER TABLE follows DROP CONSTRAINT follows_kind_check;
ALTER TABLE follows ADD CONSTRAINT follows_kind_check CHECK (kind IN ('area', 'report', 'place'));
ALTER TABLE follows DROP CONSTRAINT follows_check;
ALTER TABLE follows ADD CONSTRAINT follows_check CHECK (
    (kind = 'report' AND report_id IS NOT NULL AND latitude IS NULL AND longitude IS NULL AND radius_m IS NULL AND name IS NULL) OR
    (kind = 'area' AND report_id IS NULL AND latitude IS NOT NULL AND longitude IS NOT NULL AND radius_m IS NOT NULL AND name IS NULL) OR
    (kind = 'place' AND report_id IS NULL AND latitude IS NOT NULL AND longitude IS NOT NULL AND radius_m IS NOT NULL
        AND name IS NOT NULL AND icon IS NOT NULL)
);
DROP INDEX idx_follows_device_id;
CREATE INDEX idx_follows_device_kind ON follows (device_id, kind);

-- SOS requests. Private: only the requester device and the assigned helper
-- device can read one; helpers browsing nearby requests get a redacted view.
CREATE TABLE sos_requests (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id         text NOT NULL,
    client_id         text CHECK (client_id IS NULL OR length(client_id) BETWEEN 8 AND 64),
    type              text NOT NULL CHECK (type IN (
        'trapped', 'elderly_or_patient', 'vehicle_stalled', 'need_boat',
        'need_high_vehicle', 'need_food_water', 'need_shelter', 'other')),
    description       text CHECK (description IS NULL OR length(description) <= 1000),
    latitude          double precision NOT NULL CHECK (latitude BETWEEN -90 AND 90),
    longitude         double precision NOT NULL CHECK (longitude BETWEEN -180 AND 180),
    people_count      integer CHECK (people_count IS NULL OR people_count BETWEEN 1 AND 500),
    contact_phone     text CHECK (contact_phone IS NULL OR length(contact_phone) <= 32),
    status            text NOT NULL CHECK (status IN ('waiting', 'matched', 'on_the_way', 'arrived', 'completed', 'cancelled')),
    helper_device_id  text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    closed_at         timestamptz,
    CHECK ((status = 'waiting') = (helper_device_id IS NULL) OR status IN ('completed', 'cancelled'))
);
CREATE UNIQUE INDEX idx_sos_client_id ON sos_requests (device_id, client_id) WHERE client_id IS NOT NULL;
-- At most one open request per device, enforced by the database.
CREATE UNIQUE INDEX idx_sos_one_open_per_device ON sos_requests (device_id) WHERE status NOT IN ('completed', 'cancelled');
CREATE INDEX idx_sos_waiting_lat_lng ON sos_requests (latitude, longitude) WHERE status = 'waiting';
CREATE INDEX idx_sos_device ON sos_requests (device_id, created_at DESC);
CREATE INDEX idx_sos_helper ON sos_requests (helper_device_id, created_at DESC) WHERE helper_device_id IS NOT NULL;

-- Status timeline / audit trail of every SOS transition.
CREATE TABLE sos_events (
    id          bigserial PRIMARY KEY,
    sos_id      uuid NOT NULL REFERENCES sos_requests (id) ON DELETE CASCADE,
    status      text NOT NULL,
    actor       text NOT NULL CHECK (actor IN ('requester', 'helper')),
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_sos_events_sos ON sos_events (sos_id, created_at);

-- Helper-mode opt-in, one row per device.
CREATE TABLE helpers (
    device_id      text PRIMARY KEY,
    active         boolean NOT NULL DEFAULT false,
    capabilities   text[] NOT NULL DEFAULT '{}',
    radius_m       integer NOT NULL CHECK (radius_m IN (1000, 3000, 5000, 10000)),
    display_name   text CHECK (display_name IS NULL OR length(display_name) <= 60),
    contact_phone  text CHECK (contact_phone IS NULL OR length(contact_phone) <= 32),
    latitude       double precision CHECK (latitude BETWEEN -90 AND 90),
    longitude      double precision CHECK (longitude BETWEEN -180 AND 180),
    location_at    timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CHECK (capabilities <@ ARRAY['high_vehicle', 'boat', 'first_aid', 'food_water', 'vehicle_repair', 'towing', 'shelter', 'other']::text[])
);
CREATE INDEX idx_helpers_active_lat_lng ON helpers (latitude, longitude) WHERE active;
CREATE INDEX idx_helpers_capabilities ON helpers USING gin (capabilities) WHERE active;

-- Curated emergency / important places (hospitals, shelters, boat points...).
CREATE TABLE important_places (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    category     text NOT NULL CHECK (category IN (
        'hospital', 'shelter', 'food_water', 'rescue', 'police', 'fuel', 'vehicle_repair', 'boat_point', 'other')),
    latitude     double precision NOT NULL CHECK (latitude BETWEEN -90 AND 90),
    longitude    double precision NOT NULL CHECK (longitude BETWEEN -180 AND 180),
    address      text CHECK (address IS NULL OR length(address) <= 300),
    status       text NOT NULL CHECK (status IN ('open', 'closed', 'full', 'unknown')),
    description  text CHECK (description IS NULL OR length(description) <= 2000),
    contact      text CHECK (contact IS NULL OR length(contact) <= 120),
    source       text CHECK (source IS NULL OR length(source) <= 200),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_important_places_lat_lng ON important_places (latitude, longitude);
CREATE INDEX idx_important_places_category_status ON important_places (category, status);

-- Official announcements entered manually by an operator (no parsing).
-- The affected area is optional: a point, or a point + radius circle.
CREATE TABLE announcements (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title         text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    body          text NOT NULL CHECK (length(body) BETWEEN 1 AND 5000),
    type          text NOT NULL CHECK (type IN (
        'flood_warning', 'evacuation', 'road_closure', 'water_release', 'weather', 'shelter_info', 'general')),
    severity      text NOT NULL CHECK (severity IN ('low', 'moderate', 'high', 'critical')),
    source_name   text NOT NULL CHECK (length(source_name) BETWEEN 1 AND 200),
    source_url    text CHECK (source_url IS NULL OR length(source_url) <= 500),
    latitude      double precision CHECK (latitude BETWEEN -90 AND 90),
    longitude     double precision CHECK (longitude BETWEEN -180 AND 180),
    radius_m      integer CHECK (radius_m IS NULL OR radius_m BETWEEN 100 AND 200000),
    starts_at     timestamptz NOT NULL,
    ends_at       timestamptz,
    published_at  timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CHECK ((latitude IS NULL) = (longitude IS NULL)),
    CHECK (radius_m IS NULL OR latitude IS NOT NULL),
    CHECK (ends_at IS NULL OR ends_at > starts_at)
);
CREATE INDEX idx_announcements_live ON announcements (starts_at, ends_at) WHERE published_at IS NOT NULL;
CREATE INDEX idx_announcements_lat_lng ON announcements (latitude, longitude) WHERE published_at IS NOT NULL AND latitude IS NOT NULL;

-- "Report a problem" complaints about an incident. One per device per report.
CREATE TABLE moderation_reports (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    report_id    uuid NOT NULL REFERENCES reports (id) ON DELETE CASCADE,
    device_id    text NOT NULL,
    reason       text NOT NULL CHECK (reason IN (
        'false_information', 'wrong_location', 'duplicate', 'outdated',
        'inappropriate_image', 'spam', 'privacy', 'other')),
    details      text CHECK (details IS NULL OR length(details) <= 1000),
    status       text NOT NULL CHECK (status IN ('pending', 'resolved', 'dismissed')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    resolved_at  timestamptz,
    UNIQUE (report_id, device_id)
);
CREATE INDEX idx_moderation_pending ON moderation_reports (report_id, created_at) WHERE status = 'pending';
CREATE INDEX idx_moderation_device ON moderation_reports (device_id, created_at);
