-- +goose NO TRANSACTION
-- Finds the newest grouped event of an alert, so a recovery event can be
-- linked to the group its alert is in. events.fingerprint identifies one
-- alert, the same for its firing and its resolved events.

-- +goose Up
DROP INDEX CONCURRENTLY IF EXISTS events_fingerprint;

CREATE INDEX CONCURRENTLY events_fingerprint ON events (fingerprint, received_at);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS events_fingerprint;
