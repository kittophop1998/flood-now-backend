-- A "still happening" confirmation may also update the report's condition
-- (severity, water depth, passability, photo); it is recorded as an
-- "updated" event instead of "confirmed" so followers hear what changed.
ALTER TABLE report_events DROP CONSTRAINT report_events_kind_check;
ALTER TABLE report_events ADD CONSTRAINT report_events_kind_check
    CHECK (kind IN ('created', 'confirmed', 'updated', 'resolved', 'reopened', 'hidden', 'unhidden'));
