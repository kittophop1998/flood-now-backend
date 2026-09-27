-- Lightweight social reactions on community reports (like / support). Purely
-- social feedback: never read by severity, freshness, confirmation, safe
-- route or moderation logic. Additive only.
CREATE TABLE report_reactions (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    report_id   uuid NOT NULL REFERENCES reports (id) ON DELETE CASCADE,
    device_id   text NOT NULL,
    type        text NOT NULL CHECK (type IN ('like', 'support')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (report_id, device_id)
);

-- Covers the per-report like/support counts every report read computes
-- (index-only scan); the unique index above serves per-device lookups.
CREATE INDEX idx_report_reactions_report_type ON report_reactions (report_id, type);
