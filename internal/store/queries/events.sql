-- name: InsertEvent :one
-- InsertEvent stores an event and returns its ID. When the intake already
-- has an event with the same key, it inserts nothing and returns no row.
INSERT INTO events (
  id, intake, key, kind, sender_kind, sender_id, sender_name, trusted,
  alert_name, title, body, service, severity, labels, links, fingerprint,
  slack, raw, status, skip_reason, starts_at, ends_at, received_at
)
VALUES (
  @id,
  @intake,
  @key,
  @kind,
  sqlc.narg(sender_kind),
  sqlc.narg(sender_id),
  sqlc.narg(sender_name),
  @trusted,
  sqlc.narg(alert_name),
  sqlc.narg(title),
  sqlc.narg(body),
  sqlc.narg(service),
  sqlc.narg(severity),
  @labels,
  sqlc.narg(links),
  @fingerprint,
  sqlc.narg(slack),
  sqlc.narg(raw),
  @status,
  sqlc.narg(skip_reason),
  sqlc.narg(starts_at),
  sqlc.narg(ends_at),
  @received_at
)
ON CONFLICT (intake, key) DO NOTHING
RETURNING id;

-- name: GetEvent :one
SELECT * FROM events WHERE id = @id;

-- name: ListEventsByID :many
-- ListEventsByID returns the events with ids, in the order they arrived.
SELECT * FROM events WHERE id = ANY (@ids::uuid[]) ORDER BY received_at, id;

-- name: MarkEventRouted :execrows
-- MarkEventRouted records the route and group of a received event and sets
-- it to routed. An event that is no longer received is left as it is, and
-- no row is counted, so routing the same event twice changes nothing.
UPDATE events
SET status = 'routed', route = @route, group_id = @group_id
WHERE id = @id AND status = 'received';
