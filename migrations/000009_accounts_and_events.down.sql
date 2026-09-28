-- Lossy: user-owned rows that the older schema can't hold are removed
-- (per-user reactions, saved places without a device, community events).
DROP TABLE community_events;

ALTER TABLE sos_requests DROP COLUMN user_id;

DELETE FROM follows WHERE device_id IS NULL;
DROP INDEX idx_follows_user_places;
ALTER TABLE follows DROP CONSTRAINT follows_owner_check;
ALTER TABLE follows DROP COLUMN user_id;
ALTER TABLE follows ALTER COLUMN device_id SET NOT NULL;

DELETE FROM report_reactions WHERE device_id IS NULL;
DROP INDEX idx_report_reactions_user;
ALTER TABLE report_reactions DROP CONSTRAINT report_reactions_owner_check;
ALTER TABLE report_reactions DROP COLUMN user_id;
ALTER TABLE report_reactions ALTER COLUMN device_id SET NOT NULL;

ALTER TABLE reports DROP COLUMN user_id;

DROP TABLE user_sessions;
DROP TABLE users;
