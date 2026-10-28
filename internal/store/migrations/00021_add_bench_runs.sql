-- Benchmark runs and the result of each run of each scenario, written by
-- `sidekick bench run --db` for the web UI to read.

-- +goose Up
CREATE TABLE bench_runs (
  id               uuid PRIMARY KEY,
  label            text,
  started_at       timestamptz NOT NULL,
  finished_at      timestamptz NOT NULL,
  sidekick_version text NOT NULL,
  git_sha          text NOT NULL,
  -- The start of the SHA-256 of the config file.
  config_hash      text NOT NULL,
  -- The connector and model settings of each role, by role.
  connectors       jsonb NOT NULL,
  -- The version of each strategy the scenarios use, by name.
  strategies       jsonb NOT NULL,
  scenario_set     text NOT NULL,
  repeats          int NOT NULL,
  -- The run totals, intervals, thresholds and prompt versions, as in
  -- report.json.
  summary          jsonb NOT NULL,
  -- passed, or failed when a threshold failed.
  status           text NOT NULL,
  -- cli, ci or gate
  triggered_by     text NOT NULL
);

CREATE TABLE bench_results (
  run_id           uuid NOT NULL REFERENCES bench_runs (id) ON DELETE CASCADE,
  scenario_id      text NOT NULL,
  -- 1 for the first run of the scenario, counting up.
  repeat           int NOT NULL,
  verdict          text,
  expected_verdict text NOT NULL,
  verdict_correct  boolean NOT NULL,
  -- Null when the run was not graded, such as a scenario without a rubric.
  root_cause_score real,
  required_met     boolean,
  -- The judge's grade and reason for each rubric item, or why it gave
  -- none. It is model output, so it is untrusted.
  judge_reason     text,
  -- Null when the scenario has no evidence checks.
  evidence_score   real,
  cost_micros      bigint NOT NULL,
  duration_ms      int NOT NULL,
  input_tokens     bigint NOT NULL,
  output_tokens    bigint NOT NULL,
  tool_calls       int NOT NULL,
  PRIMARY KEY (run_id, scenario_id, repeat)
);

-- +goose Down
DROP TABLE bench_results;
DROP TABLE bench_runs;
