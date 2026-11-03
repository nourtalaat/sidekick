-- Whether the proposals of one run of a benchmark scenario were correct.
-- Null when the scenario does not score proposals, and for results stored
-- before proposals were scored.
--
-- This adds one nullable column and drops nothing.

-- +goose Up
ALTER TABLE bench_results ADD COLUMN proposal_correct boolean;

-- +goose Down
ALTER TABLE bench_results DROP COLUMN proposal_correct;
