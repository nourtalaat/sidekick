-- name: InsertStep :exec
-- InsertStep stores one step. Take its seq with AdvanceInvestigation in the
-- same transaction.
INSERT INTO steps (
  investigation_id, seq, run, kind, parts, source, tool, tool_call_id, status, error, evidence,
  connector, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_micros,
  started_at, duration_ms, budget
)
VALUES (
  @investigation_id, @seq, @run, @kind, @parts, sqlc.narg(source), sqlc.narg(tool), sqlc.narg(tool_call_id),
  @status, sqlc.narg(error), sqlc.narg(evidence), sqlc.narg(connector), sqlc.narg(model),
  sqlc.narg(input_tokens), sqlc.narg(output_tokens), sqlc.narg(cache_read_tokens), sqlc.narg(cache_write_tokens),
  sqlc.narg(cost_micros), @started_at, @duration_ms, sqlc.narg(budget)
);

-- name: ListSteps :many
-- ListSteps returns the steps of an investigation in order.
SELECT * FROM steps WHERE investigation_id = @investigation_id ORDER BY seq;
