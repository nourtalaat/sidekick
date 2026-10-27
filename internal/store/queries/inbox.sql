-- name: InsertInboxItem :exec
-- InsertInboxItem adds an item for an investigation, waiting to be
-- delivered at its next model turn.
INSERT INTO investigation_inbox (investigation_id, kind, body, created_at)
VALUES (@investigation_id, @kind, @body, now());

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
