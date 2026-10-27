-- +goose NO TRANSACTION
-- Finds the open groups of a few services, newest event first, so an event
-- can join the open group of the same service or of a service it depends
-- on, or that depends on it. Only open groups are indexed, and there are
-- few of them. The index is built without blocking writes, which needs a
-- migration outside a transaction; a run that failed part way leaves an
-- invalid index behind, which running the migration again drops first.

-- +goose Up
DROP INDEX CONCURRENTLY IF EXISTS event_groups_open_service;

CREATE INDEX CONCURRENTLY event_groups_open_service
  ON event_groups (service, last_event_at) WHERE status = 'open';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS event_groups_open_service;
