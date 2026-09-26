-- An update was also a still_active confirmation.
UPDATE report_events SET kind = 'confirmed' WHERE kind = 'updated';
ALTER TABLE report_events DROP CONSTRAINT report_events_kind_check;
ALTER TABLE report_events ADD CONSTRAINT report_events_kind_check
    CHECK (kind IN ('created', 'confirmed', 'resolved', 'reopened', 'hidden', 'unhidden'));
