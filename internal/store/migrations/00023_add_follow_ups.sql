-- +goose NO TRANSACTION
-- Follow-up questions.
--
-- A key for inbox items that can arrive twice, such as a follow-up
-- question from a Slack reply that Slack sends again. With the key, the
-- second insert adds nothing. Items without a key, such as new events, are
-- not checked. The index is built without blocking writes, which needs a
-- migration outside a transaction; a run that failed part way leaves an
-- invalid index that running the migration again drops and builds anew.
--
-- The kind of the investigation's current run: investigate for a full
-- investigation, follow_up while a follow-up run answers questions. The
-- run number only counts runs, so it cannot tell them apart. The check is
-- added without checking the rows first, then checked on its own, which
-- does not block writes; every existing row has the default.

-- +goose Up
-- For follow-ups from Slack, the Slack event ID of the reply.
ALTER TABLE investigation_inbox ADD COLUMN IF NOT EXISTS key text;

DROP INDEX CONCURRENTLY IF EXISTS investigation_inbox_key;

CREATE UNIQUE INDEX CONCURRENTLY investigation_inbox_key
  ON investigation_inbox (investigation_id, key) WHERE key IS NOT NULL;

ALTER TABLE investigations ADD COLUMN IF NOT EXISTS run_kind text NOT NULL DEFAULT 'investigate';

ALTER TABLE investigations DROP CONSTRAINT IF EXISTS investigations_run_kind_check;

ALTER TABLE investigations ADD CONSTRAINT investigations_run_kind_check
  CHECK (run_kind IN ('investigate', 'follow_up')) NOT VALID;

ALTER TABLE investigations VALIDATE CONSTRAINT investigations_run_kind_check;

-- +goose Down
ALTER TABLE investigations DROP CONSTRAINT IF EXISTS investigations_run_kind_check;

ALTER TABLE investigations DROP COLUMN IF EXISTS run_kind;

DROP INDEX CONCURRENTLY IF EXISTS investigation_inbox_key;

ALTER TABLE investigation_inbox DROP COLUMN IF EXISTS key;
