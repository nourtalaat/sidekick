-- +goose NO TRANSACTION
-- An index for counting how many times an action has been used today, so an
-- action type with a daily limit, such as one that creates tickets, can
-- check it before it runs. The proposals that are approved and not finished
-- are few, and the existing index on the status finds them.

-- +goose Up
CREATE INDEX CONCURRENTLY IF NOT EXISTS action_proposals_daily
  ON action_proposals (action, executed_at) WHERE status IN ('succeeded', 'undone');

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS action_proposals_daily;
