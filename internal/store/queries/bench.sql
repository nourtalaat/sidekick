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

-- name: ListBenchRuns :many
-- ListBenchRuns returns a page of benchmark runs, newest first, without
-- their scenario results: the run's settings and the metrics of its summary.
-- A page starts after the run with cursor_id that started at cursor_at.
SELECT id, label, started_at, finished_at, sidekick_version, git_sha, connectors, strategies,
       scenario_set, repeats, status, triggered_by, (summary -> 'metrics')::jsonb AS metrics
FROM bench_runs
WHERE sqlc.narg(cursor_at)::timestamptz IS NULL
   OR (started_at, id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
ORDER BY started_at DESC, id DESC
LIMIT @page_size;

-- name: GetBenchRun :one
-- GetBenchRun returns one benchmark run with its whole summary.
SELECT * FROM bench_runs WHERE id = @id;

-- name: ListBenchResults :many
-- ListBenchResults returns the result of each run of each scenario of a
-- benchmark run, without the judge's reasons, which are model output.
SELECT scenario_id, repeat, verdict, expected_verdict, verdict_correct, root_cause_score, required_met,
       evidence_score, cost_micros, duration_ms, input_tokens, output_tokens, tool_calls
FROM bench_results
WHERE run_id = @run_id
ORDER BY scenario_id, repeat;

-- name: SummarizeBenchScenarios :many
-- SummarizeBenchScenarios counts, for each scenario of a benchmark run, its
-- runs, those with a correct verdict, and the root-cause scores of the runs
-- that were graded, as their number and their sum.
SELECT scenario_id, expected_verdict,
       count(*)::int AS runs,
       (count(*) FILTER (WHERE verdict_correct))::int AS correct,
       count(root_cause_score)::int AS graded,
       coalesce(sum(root_cause_score), 0)::float8 AS root_cause_sum
FROM bench_results
WHERE run_id = @run_id
GROUP BY scenario_id, expected_verdict
ORDER BY scenario_id;
