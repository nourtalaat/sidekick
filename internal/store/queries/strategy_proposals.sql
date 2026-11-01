-- name: CountWrongFeedbackForVersion :one
-- CountWrongFeedbackForVersion counts the ratings of wrong on a verdict or a
-- finding, changed since the given time, on investigations that ran the
-- strategy at that exact version. It reads the recent wrong ratings through
-- feedback_wrong_recent and finds each investigation by its primary key.
SELECT count(*)::int AS wrong
FROM feedback f
JOIN investigations i ON i.id = f.investigation_id
WHERE f.rating = 'wrong' AND f.target_kind IN ('verdict', 'finding')
  AND f.updated_at >= @since
  AND i.strategy = @strategy AND i.strategy_version = @strategy_version;

-- name: InsertStrategyProposal :one
-- InsertStrategyProposal adds a proposal that waits for its draft. A
-- strategy version has one proposal, so it adds nothing and returns no row
-- when the version already has one.
INSERT INTO strategy_proposals (id, name, base_version, trigger_count, status, created_at, updated_at)
VALUES (@id, @name, @base_version, @trigger_count, 'drafting', now(), now())
ON CONFLICT (name, base_version) DO NOTHING
RETURNING id;

-- name: GetStrategyProposal :one
SELECT * FROM strategy_proposals WHERE id = @id;

-- name: LockStrategyProposal :one
-- LockStrategyProposal returns a proposal and holds a lock on its row until
-- the transaction ends, so two reviewers act on it in turn.
SELECT * FROM strategy_proposals WHERE id = @id FOR UPDATE;

-- name: LockPassedStrategyProposal :one
-- LockPassedStrategyProposal returns a proposal and locks it only when its
-- gate passed and nobody has decided on it yet. It is how a strategy version
-- proves it comes from a proposal that may be applied.
SELECT * FROM strategy_proposals WHERE id = @id AND status = 'passed' AND gate_result = 'passed' FOR UPDATE;

-- name: ListStrategyProposals :many
-- ListStrategyProposals returns a page of proposals, newest first, without
-- their bodies. A page starts after the proposal with cursor_id created at
-- cursor_at. With a status it reads strategy_proposals_status_created, and
-- without one strategy_proposals_created.
SELECT id, name, base_version, base_origin, rationale, trigger_count, status, status_detail, gate_result,
       bench_run_id, baseline_run_id, reviewer,
       created_at, updated_at, decided_at
FROM strategy_proposals
WHERE (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
  AND (sqlc.narg(name)::text IS NULL OR name = sqlc.narg(name)::text)
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT @page_size;

-- name: SetStrategyProposalDrafted :execrows
-- SetStrategyProposalDrafted stores the draft of a proposal that waits for
-- it, and makes it pending.
UPDATE strategy_proposals
SET base_origin = @base_origin, base_body = @base_body, body = @body, rationale = @rationale,
    feedback_ids = @feedback_ids, status = 'pending', updated_at = now()
WHERE id = @id AND status = 'drafting';

-- name: SetStrategyProposalDraftFailed :execrows
-- SetStrategyProposalDraftFailed ends a proposal that waits for its draft
-- because no draft can be made.
UPDATE strategy_proposals
SET status = 'draft_failed', status_detail = @detail, updated_at = now()
WHERE id = @id AND status = 'drafting';

-- name: SetStrategyProposalBenchmarking :execrows
-- SetStrategyProposalBenchmarking marks the start of the gate, and keeps the
-- hash of the scenarios it runs.
UPDATE strategy_proposals
SET status = 'benchmarking', gate_set_hash = @gate_set_hash, updated_at = now()
WHERE id = @id AND status IN ('pending', 'benchmarking');

-- name: SetStrategyProposalGate :execrows
-- SetStrategyProposalGate stores the end of the gate: passed, or failed with
-- the reason.
UPDATE strategy_proposals
SET status = @status, gate_result = @gate_result, gate_detail = @gate_detail,
    bench_run_id = sqlc.narg(bench_run_id), baseline_run_id = sqlc.narg(baseline_run_id), updated_at = now()
WHERE id = @id AND status = 'benchmarking';

-- name: SetStrategyProposalGateError :execrows
-- SetStrategyProposalGateError ends a gate that could not run, such as a
-- job that died.
UPDATE strategy_proposals
SET status = 'failed', gate_result = 'error', gate_detail = @gate_detail, updated_at = now()
WHERE id = @id AND status IN ('pending', 'benchmarking');

-- name: ResetStrategyProposalForEdit :execrows
-- ResetStrategyProposalForEdit replaces the body of a proposal whose gate
-- has ended and sends it through the gate again.
UPDATE strategy_proposals
SET body = @body, status = 'pending', gate_result = NULL, gate_detail = NULL, gate_set_hash = NULL,
    bench_run_id = NULL, baseline_run_id = NULL, reviewer = sqlc.narg(reviewer), updated_at = now()
WHERE id = @id AND status IN ('passed', 'failed');

-- name: SetStrategyProposalDecision :execrows
-- SetStrategyProposalDecision records that a reviewer rejected, approved or
-- applied the proposal.
UPDATE strategy_proposals
SET status = @status, reviewer = sqlc.narg(reviewer), decided_at = now(), updated_at = now()
WHERE id = @id;

-- name: ListWrongFeedbackForVersion :many
-- ListWrongFeedbackForVersion returns the newest ratings of wrong on a
-- verdict or finding, changed since the given time, on investigations that
-- ran the strategy at that exact version, with what the investigation said.
SELECT f.id, f.investigation_id, f.target_kind, f.target_ref, f.reason, f.correct_verdict, f.correct_cause,
       f.updated_at, i.verdict, coalesce(i.report ->> 'summary', '')::text AS summary
FROM feedback f
JOIN investigations i ON i.id = f.investigation_id
WHERE f.rating = 'wrong' AND f.target_kind IN ('verdict', 'finding')
  AND f.updated_at >= @since
  AND i.strategy = @strategy AND i.strategy_version = @strategy_version
ORDER BY f.updated_at DESC
LIMIT @max_rows;

-- name: ListFeedbackByIDs :many
-- ListFeedbackByIDs returns feedback by its IDs, for showing what a proposal
-- was drafted from.
SELECT f.id, f.investigation_id, f.target_kind, f.target_ref, f.rating, f.reason, f.correct_verdict,
       f.correct_cause, f.updated_at
FROM feedback f
WHERE f.id = ANY (@ids::uuid[])
ORDER BY f.updated_at DESC, f.id;

-- name: ListConfirmedExamples :many
-- ListConfirmedExamples returns the newest investigations of a strategy
-- whose verdict people confirmed, for the examples of a draft. It reads
-- investigations_strategy_confirmed.
SELECT id, verdict, confirmed_verdict, confirmed_cause, coalesce(report ->> 'summary', '')::text AS summary
FROM investigations
WHERE strategy = @strategy AND confirmed_verdict IS NOT NULL
ORDER BY confirmed_at DESC
LIMIT @max_rows;

-- name: InsertStrategyGateRun :exec
-- InsertStrategyGateRun keeps a finished run of a gate. A run written twice
-- keeps the first.
INSERT INTO strategy_gate_runs (proposal_id, arm, scenario_id, repeat, result, cost_micros, created_at)
VALUES (@proposal_id, @arm, @scenario_id, @repeat, @result, @cost_micros, now())
ON CONFLICT (proposal_id, arm, scenario_id, repeat) DO NOTHING;

-- name: ListStrategyGateRuns :many
-- ListStrategyGateRuns returns the finished runs of a gate through the
-- primary key.
SELECT arm, scenario_id, repeat, result, cost_micros
FROM strategy_gate_runs
WHERE proposal_id = @proposal_id
ORDER BY arm, scenario_id, repeat;

-- name: DeleteStrategyGateRuns :exec
DELETE FROM strategy_gate_runs WHERE proposal_id = @proposal_id;
