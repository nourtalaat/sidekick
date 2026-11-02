-- +goose NO TRANSACTION
-- Indexes for counting how many times an action has been used, so an action
-- type with a daily limit, such as one that creates tickets, can check it
-- before it runs: the proposals that succeeded in a day, and the ones that
-- were approved and are not finished yet.

-- +goose Up
CREATE INDEX CONCURRENTLY IF NOT EXISTS action_proposals_daily
  ON action_proposals (action, executed_at) WHERE status IN ('succeeded', 'undone');

CREATE INDEX CONCURRENTLY IF NOT EXISTS action_proposals_inflight
  ON action_proposals (action, decided_at) WHERE status IN ('approved', 'executing');

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS action_proposals_inflight;
DROP INDEX CONCURRENTLY IF EXISTS action_proposals_daily;
