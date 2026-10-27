-- +goose NO TRANSACTION
-- A key for inbox items that can arrive twice, such as a follow-up
-- question from a Slack reply that Slack sends again. With the key, the
-- second insert adds nothing. Items without a key, such as new events, are
-- not checked. The index is built without blocking writes, which needs a
-- migration outside a transaction; a run that failed part way leaves an
-- invalid index that running the migration again drops and builds anew.

-- +goose Up
-- For follow-ups from Slack, the Slack event ID of the reply.
ALTER TABLE investigation_inbox ADD COLUMN IF NOT EXISTS key text;

DROP INDEX CONCURRENTLY IF EXISTS investigation_inbox_key;

CREATE UNIQUE INDEX CONCURRENTLY investigation_inbox_key
  ON investigation_inbox (investigation_id, key) WHERE key IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS investigation_inbox_key;

ALTER TABLE investigation_inbox DROP COLUMN IF EXISTS key;
