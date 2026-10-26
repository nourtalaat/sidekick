-- The job queue: one table for every background job. internal/queue claims
-- jobs with leases, renews and reaps them, and retries failed ones.

-- +goose Up
CREATE TABLE jobs (
  id           bigserial PRIMARY KEY,
  -- route_event, investigate, follow_up, publish, execute_action,
  -- draft_lesson, propose_strategy, bench_gate, close_group, watch_tick,
  -- rollup, retention
  kind         text NOT NULL,
  -- The route the job belongs to, for per-route concurrency limits.
  route        text,
  payload      jsonb NOT NULL,
  unique_key   text,
  -- pending, running, done, dead
  status       text NOT NULL DEFAULT 'pending',
  -- Lower numbers are claimed first.
  priority     smallint NOT NULL DEFAULT 100,
  run_at       timestamptz NOT NULL DEFAULT now(),
  -- Real failures and lease expiries only; a hand-back at shutdown does not
  -- count.
  attempts     int NOT NULL DEFAULT 0,
  max_attempts int NOT NULL DEFAULT 5,
  -- From job_claim_seq, new on every claim, and used for fencing.
  claim_token  bigint,
  locked_by    text,
  locked_until timestamptz,
  last_error   text,
  -- Links the job's trace to the request that queued it.
  traceparent  text,
  created_at   timestamptz NOT NULL DEFAULT now(),
  finished_at  timestamptz
);

-- Only ever increases, and is shared by all jobs.
CREATE SEQUENCE job_claim_seq;

CREATE INDEX jobs_ready ON jobs (priority, run_at) WHERE status = 'pending';
CREATE INDEX jobs_leases ON jobs (locked_until) WHERE status = 'running';
CREATE UNIQUE INDEX jobs_active_key ON jobs (unique_key) WHERE status IN ('pending', 'running');

-- +goose Down
DROP SEQUENCE job_claim_seq;
DROP TABLE jobs;
