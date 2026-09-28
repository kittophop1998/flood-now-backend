-- Guest-first accounts + community events. See docs/database.md.
-- Additive: every existing row stays valid; anonymous (device-scoped) rows
-- keep working where the feature is still open to guests.

-- Minimal email/password accounts. No profile beyond a public display name.
CREATE TABLE users (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email          text NOT NULL CHECK (length(email) BETWEEN 3 AND 254),
    password_hash  text NOT NULL,
    display_name   text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 60),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_users_email ON users (lower(email));

-- Opaque bearer sessions; only the SHA-256 of the token is stored.
CREATE TABLE user_sessions (
    token_hash  text PRIMARY KEY,
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL
);
CREATE INDEX idx_user_sessions_user ON user_sessions (user_id);
CREATE INDEX idx_user_sessions_expires ON user_sessions (expires_at);

-- Signed-in reporter of a report (NULL = guest). Never exposed publicly.
ALTER TABLE reports ADD COLUMN user_id uuid REFERENCES users (id) ON DELETE SET NULL;

-- Reactions are per user now. Older anonymous (device) rows keep counting;
-- new rows carry user_id and no device.
ALTER TABLE report_reactions ALTER COLUMN device_id DROP NOT NULL;
ALTER TABLE report_reactions ADD COLUMN user_id uuid REFERENCES users (id) ON DELETE CASCADE;
ALTER TABLE report_reactions ADD CONSTRAINT report_reactions_owner_check CHECK (device_id IS NOT NULL OR user_id IS NOT NULL);
CREATE UNIQUE INDEX idx_report_reactions_user ON report_reactions (report_id, user_id) WHERE user_id IS NOT NULL;

-- Saved places (follows of kind 'place') belong to a user. Area/report
-- follows stay device-scoped; a user's place may have no device.
ALTER TABLE follows ALTER COLUMN device_id DROP NOT NULL;
ALTER TABLE follows ADD COLUMN user_id uuid REFERENCES users (id) ON DELETE CASCADE;
ALTER TABLE follows ADD CONSTRAINT follows_owner_check CHECK (
    (kind = 'place' AND (user_id IS NOT NULL OR device_id IS NOT NULL)) OR
    (kind <> 'place' AND device_id IS NOT NULL AND user_id IS NULL)
);
CREATE INDEX idx_follows_user_places ON follows (user_id, created_at) WHERE kind = 'place';

-- Signed-in requester of an SOS (NULL on requests made before accounts).
ALTER TABLE sos_requests ADD COLUMN user_id uuid REFERENCES users (id) ON DELETE SET NULL;

-- Community events (temple fairs, markets, walking streets…): public
-- content owned by a user, a separate domain from incident reports.
-- 'ended' is derived from end_at, never stored.
CREATE TABLE community_events (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id  uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title          text NOT NULL CHECK (length(title) BETWEEN 1 AND 120),
    description    text CHECK (description IS NULL OR length(description) <= 2000),
    category       text NOT NULL CHECK (category IN (
        'temple_fair', 'market', 'fair', 'walking_street', 'community', 'festival', 'concert', 'other')),
    latitude       double precision NOT NULL CHECK (latitude BETWEEN -90 AND 90),
    longitude      double precision NOT NULL CHECK (longitude BETWEEN -180 AND 180),
    location_name  text CHECK (location_name IS NULL OR length(location_name) <= 200),
    start_at       timestamptz NOT NULL,
    end_at         timestamptz NOT NULL,
    image_key      text,
    status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'cancelled')),
    cancelled_at   timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CHECK (end_at > start_at),
    CHECK ((status = 'cancelled') = (cancelled_at IS NOT NULL))
);
CREATE INDEX idx_community_events_window ON community_events (status, start_at, end_at);
CREATE INDEX idx_community_events_owner ON community_events (owner_user_id, created_at DESC);
-- Map viewport: bbox prefilter over events that haven't ended.
CREATE INDEX idx_community_events_lat_lng ON community_events (latitude, longitude) INCLUDE (end_at);
