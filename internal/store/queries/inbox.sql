-- name: InsertInboxItem :exec
-- InsertInboxItem adds an item for an investigation, waiting to be
-- delivered at its next model turn.
INSERT INTO investigation_inbox (investigation_id, kind, body, created_at)
VALUES (@investigation_id, @kind, @body, now());

-- name: InsertKeyedInboxItem :execrows
-- InsertKeyedInboxItem adds an item with a key, unless the investigation
-- has an item with that key already, in which case it counts no row.
INSERT INTO investigation_inbox (investigation_id, kind, body, key, created_at)
VALUES (@investigation_id, @kind, @body, sqlc.arg(key)::text, now())
ON CONFLICT (investigation_id, key) WHERE key IS NOT NULL DO NOTHING;

-- name: HasWaitingInbox :one
-- HasWaitingInbox reports whether an item of kind waits to be delivered
-- for an investigation.
SELECT EXISTS (
  SELECT 1 FROM investigation_inbox
  WHERE investigation_id = @investigation_id AND delivered_seq IS NULL AND kind = @kind
) AS waiting;

-- name: ListWaitingInbox :many
-- ListWaitingInbox returns the items of an investigation that wait to be
-- delivered, oldest first.
SELECT * FROM investigation_inbox
WHERE investigation_id = @investigation_id AND delivered_seq IS NULL
ORDER BY id;

-- name: MarkInboxDelivered :execrows
-- MarkInboxDelivered records the step that delivered the items with ids.
-- Run it in the transaction that saves that step. Items delivered before
-- are left as they are and not counted.
UPDATE investigation_inbox
SET delivered_seq = @delivered_seq
WHERE investigation_id = @investigation_id AND id = ANY (@ids::bigint[]) AND delivered_seq IS NULL;
