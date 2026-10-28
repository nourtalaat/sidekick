-- name: InsertBenchRun :exec
-- InsertBenchRun records one `sidekick bench run --db`.
INSERT INTO bench_runs (
  id, label, started_at, finished_at, sidekick_version, git_sha, config_hash,
  connectors, strategies, scenario_set, repeats, summary, status, triggered_by
)
VALUES (
  @id, sqlc.narg(label), @started_at, @finished_at, @sidekick_version, @git_sha, @config_hash,
  @connectors, @strategies, @scenario_set, @repeats, @summary, @status, @triggered_by
);

-- name: InsertBenchResult :exec
-- InsertBenchResult records one run of one scenario of a benchmark run.
INSERT INTO bench_results (
  run_id, scenario_id, repeat, verdict, expected_verdict, verdict_correct,
  root_cause_score, required_met, judge_reason, evidence_score,
  cost_micros, duration_ms, input_tokens, output_tokens, tool_calls
)
VALUES (
  @run_id, @scenario_id, @repeat, sqlc.narg(verdict), @expected_verdict, @verdict_correct,
  sqlc.narg(root_cause_score), sqlc.narg(required_met), sqlc.narg(judge_reason), sqlc.narg(evidence_score),
  @cost_micros, @duration_ms, @input_tokens, @output_tokens, @tool_calls
);
