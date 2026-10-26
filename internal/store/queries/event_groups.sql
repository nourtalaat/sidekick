-- name: InsertEventGroup :exec
-- InsertEventGroup opens a group with one event.
INSERT INTO event_groups (
  id, fingerprint, route, service, status, opened_at, last_event_at, investigation_id
)
VALUES (
  @id, @fingerprint, @route, sqlc.narg(service), 'open', now(), @last_event_at, @investigation_id
);
