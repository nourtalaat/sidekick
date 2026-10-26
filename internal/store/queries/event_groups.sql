-- name: LockOpenEventGroup :one
-- LockOpenEventGroup returns the open group with a fingerprint and locks it
-- until the transaction ends. It returns no row when no group with the
-- fingerprint is open.
SELECT * FROM event_groups
WHERE fingerprint = @fingerprint AND status = 'open'
FOR UPDATE;

-- name: OpenEventGroup :one
-- OpenEventGroup opens a group with one event and returns its ID. It
-- returns no row, and opens nothing, when a group with the same fingerprint
-- is open already; it waits for a transaction that is opening one to end
-- first.
INSERT INTO event_groups (
  id, fingerprint, route, service, status, opened_at, last_event_at, window_until, investigation_id
)
VALUES (
  @id, @fingerprint, @route, sqlc.narg(service), 'open', now(), @last_event_at, @window_until::timestamptz, @investigation_id
)
ON CONFLICT (fingerprint) WHERE status = 'open' DO NOTHING
RETURNING id;

-- name: JoinEventGroup :one
-- JoinEventGroup adds one event to a group: it counts the event, and moves
-- the newest event time and the end of the window forward, never back.
-- GREATEST skips NULL, so a group opened without a window gets one.
UPDATE event_groups
SET event_count = event_count + 1,
    last_event_at = greatest(last_event_at, @received_at),
    window_until = greatest(window_until, @window_until::timestamptz)
WHERE id = @id
RETURNING *;

-- name: CloseEventGroup :execrows
-- CloseEventGroup closes an open group with a reason. A group that is
-- closed already is left as it is and not counted.
UPDATE event_groups
SET status = 'closed', closed_reason = @closed_reason::text
WHERE id = @id AND status = 'open';

-- name: LockEventGroup :one
-- LockEventGroup returns the group with id and locks it until the
-- transaction ends.
SELECT * FROM event_groups WHERE id = @id FOR UPDATE;
