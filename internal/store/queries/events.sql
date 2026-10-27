-- name: InsertEvent :one
-- InsertEvent stores an event and returns its ID. When the intake already
-- has an event with the same key, it inserts nothing and returns no row.
INSERT INTO events (
  id, intake, key, kind, sender_kind, sender_id, sender_name, trusted,
  alert_name, title, body, service, severity, labels, links, fingerprint,
  slack, teams, raw, status, skip_reason, starts_at, ends_at, received_at
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
  sqlc.narg(teams),
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
-- MarkEventRouted records the route of a received event and sets it to
-- routed. An event that is no longer received is left as it is, and no row
-- is counted, so routing the same event twice changes nothing.
UPDATE events
SET status = 'routed', route = @route
WHERE id = @id AND status = 'received';

-- name: MarkEventGrouped :execrows
-- MarkEventGrouped records the group of a routed event, or of a received
-- recovery event, and sets it to grouped. An event in any other status is
-- left as it is, and no row is counted.
UPDATE events
SET status = 'grouped', group_id = @group_id
WHERE id = @id AND status IN ('received', 'routed');

-- name: MarkEventSkipped :execrows
-- MarkEventSkipped sets a received event to skipped with a reason. An event
-- in any other status is left as it is, and no row is counted.
UPDATE events
SET status = 'skipped', skip_reason = @skip_reason::text
WHERE id = @id AND status = 'received';

-- name: FindGroupOfAlert :one
-- FindGroupOfAlert returns the group of the newest grouped event with a
-- fingerprint, which names one alert: the group a recovery of that alert
-- belongs to. It returns no row when no event of the alert is grouped.
SELECT group_id::uuid AS group_id FROM events
WHERE fingerprint = @fingerprint AND group_id IS NOT NULL
ORDER BY received_at DESC
LIMIT 1;
