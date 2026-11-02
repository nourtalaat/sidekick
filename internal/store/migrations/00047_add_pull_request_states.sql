-- +goose NO TRANSACTION
-- Whether the pull request of each succeeded github.pull_request proposal is
-- still open, was merged or was closed. A daily job fills it from GitHub.
-- The count of noisy alerts fixed reads the merged rows.
--
-- The index on jobs is built without blocking writes, which needs a
-- migration outside a transaction. A run that failed part way can leave an
-- invalid index behind, which running the migration again drops first.

-- +goose Up
CREATE TABLE IF NOT EXISTS pull_request_states (
  -- The proposal that opened the pull request. The row goes with it.
  proposal_id uuid PRIMARY KEY REFERENCES action_proposals (id) ON DELETE CASCADE,
  repo        text NOT NULL,
  number      bigint NOT NULL,
  url         text NOT NULL,
  state       text NOT NULL CHECK (state IN ('open', 'merged', 'closed')),
  merged_at   timestamptz,
  closed_at   timestamptz,
  -- When the job last read the pull request from GitHub.
  checked_at  timestamptz NOT NULL
);

-- Counting the merged pull requests of a period.
CREATE INDEX IF NOT EXISTS pull_request_states_merged ON pull_request_states (merged_at) WHERE state = 'merged';

-- The check_pull_requests jobs in any status, so a replica can tell whether
-- today's job has run already.
DROP INDEX CONCURRENTLY IF EXISTS jobs_check_pull_requests_key;
CREATE INDEX CONCURRENTLY jobs_check_pull_requests_key ON jobs (unique_key) WHERE kind = 'check_pull_requests';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS jobs_check_pull_requests_key;
DROP TABLE IF EXISTS pull_request_states;
