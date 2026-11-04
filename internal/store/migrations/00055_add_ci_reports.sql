-- Whether the CI of the pull request or merge request that a succeeded
-- proposal opened passed, failed, never started, could not be read or had not
-- finished in time. A job fills it from the Git host and, at the end, posts
-- one message in the investigation's thread. The table is new, so no existing
-- row changes.

-- +goose Up
CREATE TABLE IF NOT EXISTS ci_reports (
  -- The proposal that opened the request. The row goes with it.
  proposal_id uuid PRIMARY KEY REFERENCES action_proposals (id) ON DELETE CASCADE,
  -- pending until the job reaches a final state.
  state       text NOT NULL CHECK (state IN ('pending', 'passed', 'failed', 'none', 'unknown', 'timed_out')),
  -- What the card and the thread message show, as output.CIReport JSON.
  detail      jsonb NOT NULL DEFAULT '{}',
  -- How many times the job has read the CI.
  checks      integer NOT NULL DEFAULT 0,
  started_at  timestamptz NOT NULL,
  checked_at  timestamptz,
  -- Set once, when the final state is reported. A set value ends the job.
  reported_at timestamptz,
  -- The seq of the thread message in the investigation's publish state.
  seq         bigint,
  -- The outputs the message has been sent to.
  sent_to     text[] NOT NULL DEFAULT '{}'
);

-- +goose Down
DROP TABLE IF EXISTS ci_reports;
