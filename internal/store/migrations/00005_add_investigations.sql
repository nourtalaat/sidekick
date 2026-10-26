-- Investigations: one per event group. The router creates the row with the
-- route's settings as they are at that moment, so a config change later
-- does not change an investigation that has started; the investigator
-- writes the run's progress and result to it.

-- +goose Up
CREATE TABLE investigations (
  id               uuid PRIMARY KEY,
  group_id         uuid NOT NULL REFERENCES event_groups (id),
  route            text NOT NULL,
  strategy         text,
  -- The strategy's version: for built-in and file strategies, the first 16
  -- hex characters of the SHA-256 of their content.
  strategy_version text,
  -- The effective action mode: read-only, approved or read-write.
  mode             text NOT NULL,
  -- queued, running, final, failed, cancelled
  status           text NOT NULL,
  run              int NOT NULL DEFAULT 1,
  -- The seq of the next step to write.
  next_seq         int NOT NULL DEFAULT 0,
  -- The claim token of the job that owns the run. Step writes check it, so a
  -- worker that lost its lease stops writing.
  fence            bigint NOT NULL DEFAULT 0,
  verdict          text,
  confidence       text,
  report           jsonb,
  stop_reason      text,
  -- Role to connector, for example {"investigate": {"connector": "claude"}}.
  connectors       jsonb NOT NULL,
  -- The limits in effect, the lower of the route's and the strategy's:
  -- {"max_tool_calls": 40, "max_wall_time_ms": 600000}.
  budget           jsonb NOT NULL,
  input_tokens     bigint NOT NULL DEFAULT 0,
  output_tokens    bigint NOT NULL DEFAULT 0,
  cost_micros      bigint NOT NULL DEFAULT 0,
  tool_calls       int NOT NULL DEFAULT 0,
  created_at       timestamptz NOT NULL,
  started_at       timestamptz,
  first_finding_at timestamptz,
  finished_at      timestamptz
);

-- For the foreign key and for finding a group's investigation.
CREATE INDEX investigations_group ON investigations (group_id);

-- +goose Down
DROP TABLE investigations;
